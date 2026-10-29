#!/usr/bin/env python3
"""HTTP server on port 80. Every request returns 200 and a success message."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOST = "0.0.0.0"
PORT = 80
BODY = b"success\n"
GUACD_LOG = "/tmp/guacd.log"


class Handler(BaseHTTPRequestHandler):
    def _send(self, body):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path != "/guacd-log":
            self._send(BODY)
            return
        try:
            with open(GUACD_LOG, "rb") as fh:
                fh.seek(0, 2)
                fh.seek(max(0, fh.tell() - 65536))
                body = fh.read()
        except OSError as exc:
            body = f"no guacd log: {exc}\n".encode()
        self._send(body)

    def log_message(self, fmt, *args):
        print(f"[port80] {self.address_string()} {fmt % args}", flush=True)


if __name__ == "__main__":
    print(f"success server listening on {HOST}:{PORT}", flush=True)
    ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
