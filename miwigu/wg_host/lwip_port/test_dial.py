#!/usr/bin/env python3
"""A connection to 127.0.0.1:3389 must become a TCP session to 10.200.99.2:3389."""

import ctypes
import socket
import struct
import sys
from pathlib import Path


def checksum(data):
    if len(data) % 2:
        data += b"\x00"
    total = 0
    for i in range(0, len(data), 2):
        total += (data[i] << 8) + data[i + 1]
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (~total) & 0xFFFF


def ipv4_tcp(src, dst, sport, dport, seq, ack, flags, payload=b""):
    tcp_len = 20 + len(payload)
    tcp = struct.pack("!HHIIHHHH", sport, dport, seq, ack, (5 << 12) | flags, 65535, 0, 0)
    pseudo = socket.inet_aton(src) + socket.inet_aton(dst) + struct.pack("!HH", 6, tcp_len)
    tcp = tcp[:16] + struct.pack("!H", checksum(pseudo + tcp + payload)) + tcp[18:] + payload
    ip_len = 20 + len(tcp)
    ip = struct.pack(
        "!BBHHHBBH4s4s",
        0x45,
        0,
        ip_len,
        1,
        0,
        64,
        6,
        0,
        socket.inet_aton(src),
        socket.inet_aton(dst),
    )
    ip = ip[:10] + struct.pack("!H", checksum(ip)) + ip[12:]
    return ip + tcp


def load(path):
    lib = ctypes.CDLL(path)
    lib.lwip_startup.restype = ctypes.c_int
    lib.lwip_poll.restype = None
    lib.lwip_inject.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
    lib.lwip_inject.restype = ctypes.c_int
    lib.lwip_next_packet.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
    lib.lwip_next_packet.restype = ctypes.c_int
    lib.lwip_next_log.argtypes = [ctypes.c_char_p, ctypes.c_int]
    lib.lwip_next_log.restype = ctypes.c_int
    return lib


def logs(lib):
    buf = ctypes.create_string_buffer(160)
    lines = []
    while lib.lwip_next_log(buf, 160) > 0:
        lines.append(buf.value.decode(errors="replace"))
    return lines


def packets(lib):
    buf = ctypes.create_string_buffer(2048)
    out = []
    while True:
        n = lib.lwip_next_packet(buf, 2048)
        if n <= 0:
            break
        out.append(bytes(buf.raw[:n]))
    return out


def tcp_of(packet):
    ihl = (packet[0] & 0x0F) * 4
    sport, dport, seq, ack, offset_flags = struct.unpack_from("!HHIIH", packet, ihl)
    header = (offset_flags >> 12) * 4
    return sport, dport, seq, ack, offset_flags & 0x1FF, packet[ihl + header :]


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else str(Path(__file__).with_name("liblwip_port.so"))
    lib = load(path)
    if lib.lwip_startup() != 0:
        raise SystemExit("lwip_startup failed: " + "; ".join(logs(lib)))
    startup = logs(lib)
    if not any("dial " in line for line in startup):
        raise SystemExit("dial listener did not start: " + "; ".join(startup))

    client = socket.create_connection(("127.0.0.1", 3389), timeout=2)
    client.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    lib.lwip_poll()
    syns = [pkt for pkt in packets(lib) if len(pkt) >= 40 and pkt[9] == 6]
    if not syns:
        raise SystemExit("no TCP SYN was queued: " + "; ".join(logs(lib)))
    syn = syns[0]
    if socket.inet_ntoa(syn[16:20]) != "10.200.99.2":
        raise SystemExit(f"SYN destination was {socket.inet_ntoa(syn[16:20])}")
    sport, dport, seq, _, flags, _ = tcp_of(syn)
    if dport != 3389 or not flags & 0x02:
        raise SystemExit(f"unexpected SYN sport={sport} dport={dport} flags={flags:#x}")

    reply = ipv4_tcp("10.200.99.2", "10.200.99.1", 3389, sport, 1000, seq + 1, 0x12)
    raw = ctypes.create_string_buffer(reply)
    if lib.lwip_inject(raw, len(reply)) != 0:
        raise SystemExit("SYN-ACK rejected: " + "; ".join(logs(lib)))
    lib.lwip_poll()
    packets(lib)
    if not any("rdp peer connected" in line for line in logs(lib)):
        raise SystemExit("handshake did not complete")

    client.sendall(b"hello")
    lib.lwip_poll()
    data = b"".join(tcp_of(pkt)[5] for pkt in packets(lib) if len(pkt) >= 40 and pkt[9] == 6)
    if b"hello" not in data:
        raise SystemExit("payload from the local socket was not written to the peer")

    back = ipv4_tcp("10.200.99.2", "10.200.99.1", 3389, sport, 1001, seq + 1 + 5, 0x18, b"pong")
    raw = ctypes.create_string_buffer(back)
    if lib.lwip_inject(raw, len(back)) != 0:
        raise SystemExit("data packet rejected")
    lib.lwip_poll()
    packets(lib)
    got = client.recv(16)
    if got != b"pong":
        raise SystemExit(f"local socket received {got!r}")
    client.close()
    print("ok")


if __name__ == "__main__":
    main()
