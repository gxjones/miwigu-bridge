#!/usr/bin/env python3
"""HTTP server on port 80. Every request returns 200 and a success message."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOST = "0.0.0.0"
PORT = 80
BODY = b"success\n"


class Handler(BaseHTTPRequestHandler):
    def _ok(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(BODY)))
        self.end_headers()
        self.wfile.write(BODY)

    def do_GET(self):
        self._ok()

    def log_message(self, fmt, *args):
        print(f"[port80] {self.address_string()} {fmt % args}", flush=True)


if __name__ == "__main__":
    print(f"success server listening on {HOST}:{PORT}", flush=True)
    ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
