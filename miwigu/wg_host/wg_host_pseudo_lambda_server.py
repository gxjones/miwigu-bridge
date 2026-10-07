#!/usr/bin/env python3
"""Lambda MicroVM-style WebSocket front door.

A client must offer these Sec-WebSocket-Protocol values, in order:

    lambda-microvms
    lambda-microvms.authentication.<token>
    lambda-microvms.port.<N>

The token is required and not verified here (Lambda checks the JWE). N must
be the listener port. Those three values are removed, and a normal WebSocket
upgrade is forwarded to 127.0.0.1:8080. The 101 sent back to the client
selects lambda-microvms.
"""

import select
import socket
import sys

LISTEN_HOST = "10.200.0.1"
LISTEN_PORT = 8765
UPSTREAM_HOST = "127.0.0.1"
UPSTREAM_PORT = 8080

BASE = "lambda-microvms"
AUTH_PREFIX = "lambda-microvms.authentication."
PORT_PREFIX = "lambda-microvms.port."


def log(line):
    print(line, flush=True)


def read_headers(conn):
    data = b""
    while b"\r\n\r\n" not in data:
        chunk = conn.recv(4096)
        if not chunk:
            raise ConnectionError("closed")
        data += chunk
        if len(data) > 65536:
            raise ValueError("headers too large")
    head, rest = data.split(b"\r\n\r\n", 1)
    return head.decode("iso-8859-1", "replace"), rest


def header_map(head):
    fields = {}
    for line in head.split("\r\n")[1:]:
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        fields.setdefault(key.strip().lower(), value.strip())
    return fields


def protocols_of(fields):
    raw = fields.get("sec-websocket-protocol", "")
    return [part.strip() for part in raw.split(",") if part.strip()]


def reject(conn, code, reason):
    body = reason.encode("ascii", "replace")
    response = (
        f"HTTP/1.1 {code} {reason}\r\n"
        f"Content-Length: {len(body)}\r\n"
        "Connection: close\r\n"
        "\r\n"
    )
    try:
        conn.sendall(response.encode("ascii") + body)
    except OSError:
        pass


def check(head):
    """Return (status, reason, protocols) or (None, None, protocols) to forward."""
    lines = head.split("\r\n")
    if not lines or not lines[0].startswith("GET "):
        return 400, "Bad Request", []
    fields = header_map(head)
    if fields.get("upgrade", "").lower() != "websocket":
        return 400, "Bad Request", []
    offered = protocols_of(fields)
    if len(offered) < 3 or offered[0] != BASE:
        return 400, "Bad Request", offered
    token = offered[1][len(AUTH_PREFIX):] if offered[1].startswith(AUTH_PREFIX) else ""
    if not token:
        return 403, "Forbidden", offered
    if not offered[2].startswith(PORT_PREFIX):
        return 400, "Bad Request", offered
    port_text = offered[2][len(PORT_PREFIX):]
    if not port_text.isdigit() or int(port_text) != UPSTREAM_PORT:
        return 403, "Forbidden", offered
    return None, None, offered


def forward_request(head):
    """Drop the Lambda subprotocols. Keep any application protocols after them."""
    lines = head.split("\r\n")
    fields = header_map(head)
    extra = protocols_of(fields)[3:]
    out = []
    for index, line in enumerate(lines):
        if index > 0 and line.split(":", 1)[0].strip().lower() == "sec-websocket-protocol":
            continue
        out.append(line)
    if extra:
        out.append("Sec-WebSocket-Protocol: " + ", ".join(extra))
    return ("\r\n".join(out) + "\r\n\r\n").encode("iso-8859-1")


def select_protocol(head):
    lines = [line for line in head.split("\r\n") if line != ""]
    lines.append("Sec-WebSocket-Protocol: " + BASE)
    return ("\r\n".join(lines) + "\r\n\r\n").encode("iso-8859-1")


def splice(left, right):
    sockets = [left, right]
    try:
        while sockets:
            readable, _, _ = select.select(sockets, [], [], 60)
            if not readable:
                continue
            for sock in readable:
                other = right if sock is left else left
                try:
                    data = sock.recv(65536)
                except OSError:
                    return
                if not data:
                    return
                try:
                    other.sendall(data)
                except OSError:
                    return
    finally:
        for sock in (left, right):
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            sock.close()


def serve(client, addr):
    log(f"connected  {addr[0]}:{addr[1]}")
    upstream = None
    try:
        head, rest = read_headers(client)
        code, reason, offered = check(head)
        if code is not None:
            log(f"rejected  {code} {reason}")
            reject(client, code, reason)
            client.close()
            return
        try:
            upstream = socket.create_connection((UPSTREAM_HOST, UPSTREAM_PORT), timeout=5)
        except OSError as exc:
            log(f"upstream unavailable  {exc}")
            reject(client, 502, "Bad Gateway")
            client.close()
            return
        upstream.sendall(forward_request(head))
        up_head, up_rest = read_headers(upstream)
        status = up_head.split("\r\n", 1)[0]
        if " 101 " not in status:
            log(f"upstream refused  {status}")
            client.sendall((up_head + "\r\n\r\n").encode("iso-8859-1") + up_rest)
            client.close()
            upstream.close()
            return
        client.sendall(select_protocol(up_head) + up_rest)
        if rest:
            upstream.sendall(rest)
        log(f"bridged  {len(offered)} subprotocols -> {UPSTREAM_HOST}:{UPSTREAM_PORT}")
        splice(client, upstream)
        upstream = None
    except (ConnectionError, ValueError, OSError) as exc:
        log(f"error  {exc}")
        try:
            client.close()
        except OSError:
            pass
        if upstream is not None:
            upstream.close()
    log(f"disconnected  {addr[0]}:{addr[1]}")


def main():
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind((LISTEN_HOST, LISTEN_PORT))
    sock.listen(8)
    log(
        f"listening  ws://{LISTEN_HOST}:{LISTEN_PORT}/  "
        f"-> ws://{UPSTREAM_HOST}:{UPSTREAM_PORT}/"
    )
    while True:
        conn, addr = sock.accept()
        try:
            serve(conn, addr)
        except Exception as exc:
            log(f"session error  {exc}")
            conn.close()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
