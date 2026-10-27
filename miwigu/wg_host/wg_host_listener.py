#!/usr/bin/env python3
"""WebSocket listener for the forwarded MicroVM connection.

lwIP also listens on 127.0.0.1:3389 and dials the WireGuard peer, so guacd
can RDP to that address.

Lambda's proxy connects to this process. It binds 0.0.0.0:8080, accepts a
normal WebSocket upgrade (the Lambda subprotocols have already been removed),
and turns each binary frame into a WireGuard datagram for BoringTun, then
lwIP, then a host socket. Each connection is handled on its own thread. One
BoringTun session is live at a time; a new finished handshake replaces it.

Lifecycle hooks listen on 0.0.0.0:9000. In microvm mode the WireGuard keys
are not created until POST /aws/lambda-microvms/runtime/v1/run, so they are
not captured in the image snapshot.
"""

import base64
import ctypes
import hashlib
import json
import os
import select
import socket
import struct
import sys
import threading
import time
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parent
HOST = os.environ.get("WG_LISTEN_HOST", "0.0.0.0")
PORT = int(os.environ.get("WG_LISTEN_PORT", "8080"))
HOOK_HOST = os.environ.get("WG_HOOK_HOST", "0.0.0.0")
HOOK_PORT = int(os.environ.get("WG_HOOK_PORT", "9000"))
MICROVM = os.environ.get("WG_HOST_MODE", "local") == "microvm"
GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
LOG = os.environ.get("WG_HOST_LOG", str(ROOT / "wg-host-listener.log"))
TUNN_LIB = os.environ.get("TUNN_LIB", str(ROOT / "tunn_ffi" / "target" / "release" / "libtunn_ffi.so"))
LWIP_LIB = os.environ.get("LWIP_LIB", str(ROOT / "lwip_port" / "liblwip_port.so"))
OUR_ADDR = "10.200.99.1"
GUAC_CRYPT_FILE = os.environ.get("GUAC_CRYPT_FILE", "/tmp/guac-crypt.key")
# Public key of the Windows dummy tunnel (windows-dummy.conf PrivateKey).
PEER_PUBLIC = os.environ.get(
    "WG_PEER_PUBLIC", "GHhBnw7IqG3OcOcjH7lE3xXVOsNLH64SWpofim1wkE8="
).encode()
HOOK_PREFIX = "/aws/lambda-microvms/runtime/v1"
# Drop a connection that never completes the upgrade. wstoudp gives up at 10s.
HANDSHAKE_TIMEOUT = float(os.environ.get("WG_HANDSHAKE_TIMEOUT", "5"))

DONE = 0
WRITE_TO_NETWORK = 1
WIREGUARD_ERROR = 2
WRITE_TO_TUNNEL_IPV4 = 4
WRITE_TO_TUNNEL_IPV6 = 6
DST_SIZE = 65536

WG_KIND = {1: "initiation", 2: "response", 3: "cookie", 4: "transport"}


class WgResult(ctypes.Structure):
    _fields_ = [("op", ctypes.c_uint32), ("size", ctypes.c_uint64)]


log_lock = threading.Lock()


def log(line):
    stamp = datetime.now().strftime("%H:%M:%S")
    text = f"{stamp}  {line}"
    with log_lock:
        print(text, flush=True)
        try:
            with open(LOG, "a", encoding="utf-8") as fh:
                fh.write(text + "\n")
        except OSError:
            pass


def load_library():
    lib = ctypes.CDLL(TUNN_LIB)
    lib.new_tunnel.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint16]
    lib.new_tunnel.restype = ctypes.c_void_p
    lib.wireguard_public_key.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint32]
    lib.wireguard_public_key.restype = ctypes.c_int
    lib.wireguard_read.argtypes = [
        ctypes.c_void_p,
        ctypes.c_void_p,
        ctypes.c_uint32,
        ctypes.c_void_p,
        ctypes.c_uint32,
    ]
    lib.wireguard_read.restype = WgResult
    lib.wireguard_write.argtypes = lib.wireguard_read.argtypes
    lib.wireguard_write.restype = WgResult
    lib.wireguard_tick.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_uint32]
    lib.wireguard_tick.restype = WgResult
    lib.wireguard_handshake_age.argtypes = [ctypes.c_void_p]
    lib.wireguard_handshake_age.restype = ctypes.c_int64
    if ctypes.sizeof(WgResult) != 16:
        raise SystemExit(f"unexpected WgResult size {ctypes.sizeof(WgResult)}")
    return lib


def public_key(private_text):
    out = ctypes.create_string_buffer(64)
    rc = LIB.wireguard_public_key(private_text.strip().encode(), out, 64)
    if rc != 0:
        raise SystemExit("wireguard_public_key failed")
    return out.value.decode()


def write_guac_key(key):
    """Publish the guacamole-lite AES key. The guest reads it after /run."""
    key = key.strip()
    if len(key.encode("utf-8")) != 32:
        raise RuntimeError("guacCryptKey must be 32 bytes")
    fd = os.open(GUAC_CRYPT_FILE, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.write(fd, key.encode("utf-8"))
    finally:
        os.close(fd)
    os.chmod(GUAC_CRYPT_FILE, 0o600)
    log("guacamole key ready")


def start_tunnel(private_text, peer_public):
    """Create the BoringTun session and lwIP. Safe to call once."""
    global TUNNEL, LWIP, PEER_PUBLIC
    if TUNNEL is not None:
        return
    private_text = private_text.strip()
    peer_public = peer_public.strip()
    PEER_PUBLIC = peer_public.encode()
    tunnel = LIB.new_tunnel(private_text.encode(), PEER_PUBLIC, 25)
    if not tunnel:
        raise RuntimeError("new_tunnel failed")
    TUNNEL = tunnel
    LWIP = LwIP()
    log(f"wireguard ready  server public {public_key(private_text)}")


LIB = None
TUNNEL = None
DST = ctypes.create_string_buffer(DST_SIZE)
active_lock = threading.Lock()
active_conn = None
# lwIP and BoringTun are single-threaded. Only the session holder touches them.
session_lock = threading.Lock()
open_lock = threading.Lock()
open_conns = set()


def conf_value(text, section, key):
    current = None
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if line.startswith("[") and line.endswith("]"):
            current = line[1:-1].strip().lower()
            continue
        if current == section and "=" in line:
            name, value = line.split("=", 1)
            if name.strip().lower() == key:
                return value.strip()
    return None


def wireguard_peer_config():
    server_private = (ROOT / "server.key").read_text().strip()
    server_public = public_key(server_private)
    peer_conf = (ROOT / "windows-dummy.conf").read_text()
    peer_private = conf_value(peer_conf, "interface", "privatekey")
    if not peer_private:
        raise SystemExit("windows-dummy.conf is missing Interface PrivateKey")
    peer_public = public_key(peer_private)
    if peer_public != PEER_PUBLIC.decode():
        raise SystemExit(
            f"windows-dummy.conf private key is {peer_public}, Tunn expects {PEER_PUBLIC.decode()}"
        )
    return (
        "[Interface]\n"
        f"PrivateKey = {peer_private}\n"
        "Address = 10.200.99.2/32\n"
        "MTU = 1300\n"
        "\n"
        "[Peer]\n"
        f"PublicKey = {server_public}\n"
        "Endpoint = 127.0.0.1:51820\n"
        f"AllowedIPs = {OUR_ADDR}/32\n"
        "PersistentKeepalive = 25\n"
    )


def wg_kind(packet):
    if not packet:
        return "empty"
    return WG_KIND.get(packet[0], f"type {packet[0]}")


def take_result(result):
    if result.size == 0:
        return result.op, b""
    return result.op, bytes(DST.raw[: result.size])


def read_datagram(payload):
    if payload:
        src = ctypes.create_string_buffer(payload)
        size = len(payload)
    else:
        src = ctypes.create_string_buffer(1)
        size = 0
    return take_result(LIB.wireguard_read(TUNNEL, src, size, DST, DST_SIZE))


def write_packet(ip_packet):
    src = ctypes.create_string_buffer(ip_packet)
    return take_result(LIB.wireguard_write(TUNNEL, src, len(ip_packet), DST, DST_SIZE))


def ip_summary(packet):
    if len(packet) < 20 or packet[0] >> 4 != 4:
        return "not ipv4"
    header = (packet[0] & 0x0F) * 4
    proto = {1: "icmp", 6: "tcp", 17: "udp"}.get(packet[9], str(packet[9]))
    src = socket.inet_ntoa(packet[12:16])
    dst = socket.inet_ntoa(packet[16:20])
    extra = ""
    if packet[9] == 1 and len(packet) > header:
        extra = {8: " echo-request", 0: " echo-reply"}.get(packet[header], "")
    elif packet[9] == 6 and len(packet) >= header + 4:
        sport, dport = struct.unpack_from("!HH", packet, header)
        extra = f" {sport}->{dport}"
    return f"{src} -> {dst} {proto}{extra}"


class LwIP:
    def __init__(self):
        self.lib = ctypes.CDLL(LWIP_LIB)
        self.lib.lwip_startup.restype = ctypes.c_int
        self.lib.lwip_inject.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
        self.lib.lwip_inject.restype = ctypes.c_int
        self.lib.lwip_poll.restype = None
        self.lib.lwip_next_packet.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
        self.lib.lwip_next_packet.restype = ctypes.c_int
        self.lib.lwip_next_log.argtypes = [ctypes.c_char_p, ctypes.c_int]
        self.lib.lwip_next_log.restype = ctypes.c_int
        self.buf = ctypes.create_string_buffer(2048)
        self.logbuf = ctypes.create_string_buffer(160)
        if self.lib.lwip_startup() != 0:
            raise SystemExit("lwip_startup failed")
        self.drain_logs()

    def drain_logs(self):
        while self.lib.lwip_next_log(self.logbuf, 160) > 0:
            log(self.logbuf.value.decode(errors="replace"))

    def drain_packets(self):
        packets = []
        while True:
            n = self.lib.lwip_next_packet(self.buf, 2048)
            if n <= 0:
                break
            packets.append(bytes(self.buf.raw[:n]))
        self.drain_logs()
        return packets

    def ingest(self, packet):
        src = ctypes.create_string_buffer(packet)
        if self.lib.lwip_inject(src, len(packet)) != 0:
            log("lwIP rejected a packet")
        return self.drain_packets()

    def poll(self):
        self.lib.lwip_poll()
        return self.drain_packets()


LWIP = None


def wireguard_frames(ip_packets):
    frames = []
    for ip in ip_packets:
        nop, nbuf = write_packet(ip)
        if nop == WRITE_TO_NETWORK:
            frames.append(nbuf)
        elif nop == WIREGUARD_ERROR:
            log("tunn error encapsulating lwIP packet")
    return frames


def consume(payload):
    """Feed one datagram to Tunn and return packets that must go back on the WebSocket."""
    outgoing = []
    data = payload
    for _ in range(8):
        op, buf = read_datagram(data)
        data = b""
        if op == DONE:
            break
        if op == WIREGUARD_ERROR:
            log("tunn error")
            break
        if op == WRITE_TO_NETWORK:
            log(f"-> network  {len(buf)} bytes  {wg_kind(buf)}")
            outgoing.append(buf)
            continue
        if op == WRITE_TO_TUNNEL_IPV4:
            log(f"-> tunnel ipv4  {len(buf)} bytes  {ip_summary(buf)}")
            if LWIP is not None:
                outgoing.extend(wireguard_frames(LWIP.ingest(buf)))
            continue
        if op == WRITE_TO_TUNNEL_IPV6:
            log(f"-> tunnel ipv6  {len(buf)} bytes")
            continue
        break
    return outgoing


def tick():
    op, buf = take_result(LIB.wireguard_tick(TUNNEL, DST, DST_SIZE))
    if op == WRITE_TO_NETWORK and buf:
        log(f"timer -> network  {len(buf)} bytes  {wg_kind(buf)}")
        return [buf]
    if op == WIREGUARD_ERROR:
        log("tunn timer error")
    return []


def handshake(conn):
    """Read one upgrade and answer it. A silent peer is closed."""
    conn.settimeout(HANDSHAKE_TIMEOUT)
    try:
        data = b""
        while b"\r\n\r\n" not in data:
            try:
                chunk = conn.recv(4096)
            except TimeoutError:
                log("websocket handshake timed out")
                return False
            if not chunk:
                return False
            data += chunk
            if len(data) > 65536:
                return False
        header = data.split(b"\r\n\r\n", 1)[0].decode("iso-8859-1", "replace")
        lines = header.split("\r\n")
        fields = {}
        for line in lines[1:]:
            if ":" in line:
                key, value = line.split(":", 1)
                fields[key.strip().lower()] = value.strip()
        if fields.get("upgrade", "").lower() != "websocket":
            return False
        key = fields.get("sec-websocket-key", "")
        accept = base64.b64encode(hashlib.sha1((key + GUID).encode("ascii")).digest()).decode("ascii")
        response = (
            "HTTP/1.1 101 Switching Protocols\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Accept: {accept}\r\n"
            "\r\n"
        )
        conn.sendall(response.encode("ascii"))
        offered = fields.get("sec-websocket-protocol")
        if offered:
            log(f"websocket protocol {offered}")
        log(f"websocket open  path={lines[0]}")
        return True
    finally:
        conn.settimeout(None)


def recv_exact(conn, n):
    buf = b""
    while len(buf) < n:
        chunk = conn.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("closed")
        buf += chunk
    return buf


def read_frame(conn):
    b0, b1 = recv_exact(conn, 2)
    opcode = b0 & 0x0F
    masked = (b1 & 0x80) != 0
    length = b1 & 0x7F
    if length == 126:
        length = struct.unpack("!H", recv_exact(conn, 2))[0]
    elif length == 127:
        length = struct.unpack("!Q", recv_exact(conn, 8))[0]
    mask = recv_exact(conn, 4) if masked else b""
    payload = recv_exact(conn, length) if length else b""
    if masked:
        payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    return opcode, payload


def send_frame(conn, opcode, payload):
    header = bytes([0x80 | opcode])
    n = len(payload)
    if n < 126:
        header += bytes([n])
    elif n < 65536:
        header += bytes([126]) + struct.pack("!H", n)
    else:
        header += bytes([127]) + struct.pack("!Q", n)
    conn.sendall(header + payload)


def preview(payload):
    shown = payload[:24].hex()
    if len(payload) > 24:
        shown += "…"
    return shown


def still_active(conn):
    with active_lock:
        return active_conn is conn


def register(conn):
    with open_lock:
        open_conns.add(conn)


def unregister(conn):
    with open_lock:
        open_conns.discard(conn)


def take_session(conn):
    """Make conn the WireGuard session and drop the previous socket."""
    global active_conn
    with active_lock:
        old = active_conn
        active_conn = conn
    if old is not None and old is not conn:
        log("replacing websocket")
        try:
            old.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
    # Released by serve() after the session loop. Do not hold active_lock here:
    # the previous session still owns session_lock and checks active_conn.
    session_lock.acquire()


def close_active():
    """Close every WebSocket so the VM can suspend with none in flight."""
    with open_lock:
        conns = list(open_conns)
    for conn in conns:
        try:
            conn.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
    for _ in range(50):
        with open_lock:
            if not open_conns:
                return
        time.sleep(0.01)


def run_session(conn):
    session_logged = False
    while still_active(conn):
        for packet in tick():
            send_frame(conn, 0x2, packet)
        if LWIP is not None:
            for packet in wireguard_frames(LWIP.poll()):
                send_frame(conn, 0x2, packet)
        if not session_logged and LIB.wireguard_handshake_age(TUNNEL) >= 0:
            log("wireguard session up")
            session_logged = True
        ready, _, _ = select.select([conn], [], [], 0.02)
        if not ready:
            continue
        opcode, payload = read_frame(conn)
        if opcode == 0x8:
            log(f"close  {len(payload)} bytes")
            send_frame(conn, 0x8, payload[:2])
            return
        if opcode == 0x9:
            send_frame(conn, 0xA, payload)
            continue
        if opcode == 0xA:
            continue
        if opcode != 0x2:
            log(f"ignored opcode {opcode}  {len(payload)} bytes")
            continue
        log(f"binary  {len(payload)} bytes  {wg_kind(payload)}  {preview(payload)}")
        for packet in consume(payload):
            send_frame(conn, 0x2, packet)


def serve(conn, addr):
    global active_conn
    log(f"connected  {addr[0]}:{addr[1]}")
    register(conn)
    try:
        if TUNNEL is None or LWIP is None:
            log("websocket closed  tunnel not ready")
            return
        if not handshake(conn):
            log("websocket handshake failed")
            return
        take_session(conn)
        try:
            if still_active(conn):
                run_session(conn)
        finally:
            session_lock.release()
    except ConnectionError:
        log("connection closed")
    except Exception as exc:
        log(f"error  {exc}")
    finally:
        with active_lock:
            if active_conn is conn:
                active_conn = None
        unregister(conn)
        try:
            conn.close()
        except OSError:
            pass
        log(f"disconnected  {addr[0]}:{addr[1]}")


def start_from_run(body):
    if TUNNEL is not None:
        return
    if not body:
        raise RuntimeError("run payload is empty")
    payload = json.loads(body.decode())
    # RunMicrovm delivers {"microvmId", "runHookPayload"}. The keys are the inner string.
    inner = payload.get("runHookPayload")
    if isinstance(inner, str):
        if not inner:
            raise RuntimeError("runHookPayload is empty")
        payload = json.loads(inner)
    elif isinstance(inner, dict):
        payload = inner
    private = payload.get("serverPrivateKey", "")
    peer = payload.get("peerPublicKey", "")
    host_addr = payload.get("hostAddr")
    if host_addr:
        os.environ["WG_HOST_ADDR"] = str(host_addr)
    guac_key = payload.get("guacCryptKey", "")
    if guac_key:
        write_guac_key(guac_key)
    if not private or not peer:
        raise RuntimeError("run payload needs serverPrivateKey and peerPublicKey")
    start_tunnel(private, peer)


class HookHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0") or "0")
        body = self.rfile.read(length) if length else b""
        path = self.path.split("?", 1)[0]
        if path not in (
            f"{HOOK_PREFIX}/ready",
            f"{HOOK_PREFIX}/validate",
            f"{HOOK_PREFIX}/run",
            f"{HOOK_PREFIX}/resume",
            f"{HOOK_PREFIX}/suspend",
            f"{HOOK_PREFIX}/terminate",
        ):
            self.reply(404, b"not found")
            return
        name = path.rsplit("/", 1)[-1]
        try:
            if name == "run":
                start_from_run(body)
            elif name == "suspend":
                log("suspend  closing websocket")
                close_active()
            elif name == "resume":
                log(f"resume  clock {int(time.time())}")
            elif name == "terminate":
                close_active()
            self.reply(200, b"")
            log(f"hook {name}  200")
        except Exception as exc:
            log(f"hook {name}  {exc}")
            self.reply(500, str(exc).encode())

    def reply(self, code, body):
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


def start_hooks():
    server = ThreadingHTTPServer((HOOK_HOST, HOOK_PORT), HookHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    log(f"hooks  http://{HOOK_HOST}:{HOOK_PORT}{HOOK_PREFIX}/")


def main():
    global LIB
    LIB = load_library()
    if not MICROVM:
        guac_key = os.environ.get("GUAC_CRYPT_KEY", "")
        if guac_key:
            write_guac_key(guac_key)
        private = (ROOT / "server.key").read_text()
        start_tunnel(private, PEER_PUBLIC.decode())
        config = wireguard_peer_config()
        log("wireguard peer config:")
        print(config, flush=True)
        try:
            with open(LOG, "a", encoding="utf-8") as fh:
                fh.write(config)
        except OSError:
            pass
    start_hooks()
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind((HOST, PORT))
    sock.listen(8)
    log(f"listening  ws://{HOST}:{PORT}/  boringtun Tunn  lwIP")
    while True:
        conn, addr = sock.accept()
        try:
            conn.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        except OSError:
            pass
        threading.Thread(target=serve, args=(conn, addr), daemon=True).start()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
