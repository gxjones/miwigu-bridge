#!/usr/bin/env python3
"""
08_guacamole.py

Serves a Guacamole page on localhost. The page opens its own WebSocket to
guacamole-lite. On a MicroVM that socket is wss://{endpoint}/websocket and
carries the Lambda subprotocols (auth token and port 8081), because a browser
cannot set the proxy headers. Locally it is ws://127.0.0.1:8081/websocket.

The query token tells guacd to RDP to 127.0.0.1:3389, which the guest dials
out to the WireGuard peer at 10.200.99.2:3389.

  GUAC_RDP_USER=Administrator GUAC_RDP_PASSWORD=secret python scripts/08_guacamole.py

Local runner (docker run -p 8081:8081), using the key the container prints:

  GUAC_RDP_USER=Administrator GUAC_RDP_PASSWORD=secret \\
    python scripts/08_guacamole.py --local --key '<32-byte key>'
"""

import argparse
import asyncio
import base64
import json
import os
import sys
import webbrowser

import boto3
from aiohttp import web
from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

sys.path.insert(0, ".")
from config import AWS_REGION, STATE_FILE

LISTEN = "127.0.0.1:8766"
GUEST_PORT = 8081
TOKEN_EXPIRY_MINUTES = 60
DIAL_HOST = "127.0.0.1"
DIAL_PORT = "3389"

PAGE = """\
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>MicroVM RDP</title>
  <style>
    html, body { margin: 0; height: 100%; background: #111; overflow: hidden; }
    #display { width: 100%; height: 100%; }
    #status {
      position: absolute; left: 12px; bottom: 12px; color: #f4f4f4;
      font: 14px/1.4 sans-serif; background: rgba(0, 0, 0, 0.6);
      padding: 6px 10px; display: none;
    }
  </style>
</head>
<body>
  <div id="display"></div>
  <div id="status"></div>
  <script type="module">
    import Guacamole from "https://cdn.jsdelivr.net/npm/guacamole-common-js@1.5.0/dist/esm/guacamole-common.js";

    const token = __TOKEN_JSON__;
    const wsUrl = __WS_URL_JSON__;
    const protocols = __PROTOCOLS_JSON__;
    const status = document.getElementById("status");
    function show(text) {
      status.style.display = "block";
      status.textContent = text;
    }

    // WebSocketTunnel offers only "guacamole". Offer the MicroVM auth and
    // port subprotocols for that one constructor call.
    const NativeWebSocket = window.WebSocket;
    function TunnelWebSocket(url, protocol) {
      const selected = protocol === "guacamole" ? protocols : protocol;
      return new NativeWebSocket(url, selected);
    }
    TunnelWebSocket.prototype = NativeWebSocket.prototype;
    TunnelWebSocket.CONNECTING = NativeWebSocket.CONNECTING;
    TunnelWebSocket.OPEN = NativeWebSocket.OPEN;
    TunnelWebSocket.CLOSING = NativeWebSocket.CLOSING;
    TunnelWebSocket.CLOSED = NativeWebSocket.CLOSED;

    const tunnel = new Guacamole.WebSocketTunnel(wsUrl);
    const connectTunnel = tunnel.connect.bind(tunnel);
    tunnel.connect = (data) => {
      const previous = window.WebSocket;
      window.WebSocket = TunnelWebSocket;
      try {
        return connectTunnel(data);
      } finally {
        window.WebSocket = previous;
      }
    };
    const client = new Guacamole.Client(tunnel);
    const display = client.getDisplay();
    const el = display.getElement();
    document.getElementById("display").appendChild(el);

    function fit() {
      const width = display.getWidth();
      const height = display.getHeight();
      if (!width || !height) return;
      display.scale(Math.min(window.innerWidth / width, window.innerHeight / height));
    }
    display.onresize = fit;

    client.onerror = (err) => show("RDP error " + (err && (err.message || err.code) || ""));
    client.onstatechange = (state) => {
      if (state === Guacamole.Client.State.CONNECTED) {
        status.style.display = "none";
        client.sendSize(window.innerWidth, window.innerHeight);
      } else if (state === Guacamole.Client.State.DISCONNECTED) {
        show("disconnected");
      }
    };

    const mouse = new Guacamole.Mouse(el);
    mouse.onmousedown = mouse.onmouseup = mouse.onmousemove = (state) => {
      client.sendMouseState(state);
    };
    const keyboard = new Guacamole.Keyboard(document);
    keyboard.onkeydown = (keysym) => client.sendKeyEvent(1, keysym);
    keyboard.onkeyup = (keysym) => client.sendKeyEvent(0, keysym);

    let resizeTimer = 0;
    window.addEventListener("resize", () => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        client.sendSize(window.innerWidth, window.innerHeight);
      }, 200);
    });
    window.addEventListener("beforeunload", () => client.disconnect());
    client.connect("token=" + encodeURIComponent(token));
  </script>
</body>
</html>
"""


def encrypt_token(value, key):
    raw_key = key.encode("utf-8")
    if len(raw_key) != 32:
        raise SystemExit("guacamole key must be 32 bytes")
    iv = os.urandom(16)
    padder = padding.PKCS7(128).padder()
    padded = padder.update(json.dumps(value, separators=(",", ":")).encode("utf-8")) + padder.finalize()
    encryptor = Cipher(algorithms.AES(raw_key), modes.CBC(iv)).encryptor()
    encrypted = encryptor.update(padded) + encryptor.finalize()
    blob = {"iv": base64.b64encode(iv).decode("ascii"), "value": base64.b64encode(encrypted).decode("ascii")}
    return base64.b64encode(json.dumps(blob, separators=(",", ":")).encode("utf-8")).decode("ascii")


def load_state():
    state_path = os.path.join(os.path.dirname(__file__), "..", STATE_FILE)
    if not os.path.exists(state_path):
        raise SystemExit(f"{STATE_FILE} not found. Run 04_run_microvm.py first.")
    with open(state_path) as fh:
        return json.load(fh)


def create_auth_token(microvm_id, region):
    microvms = boto3.client("lambda-microvms", region_name=region)
    print(f"Creating auth token for MicroVM {microvm_id} ...")
    response = microvms.create_microvm_auth_token(
        microvmIdentifier=microvm_id,
        expirationInMinutes=TOKEN_EXPIRY_MINUTES,
        allowedPorts=[{"allPorts": {}}],
    )
    auth_token = response["authToken"]
    if isinstance(auth_token, dict):
        auth_token = auth_token["X-aws-proxy-auth"]
    return auth_token


def microvm_protocols(auth_token):
    return [
        "lambda-microvms",
        "lambda-microvms.authentication." + auth_token,
        f"lambda-microvms.port.{GUEST_PORT}",
        "guacamole",
    ]


def render_page(guac_token, ws_url, protocols):
    return (
        PAGE.replace("__TOKEN_JSON__", json.dumps(guac_token))
        .replace("__WS_URL_JSON__", json.dumps(ws_url))
        .replace("__PROTOCOLS_JSON__", json.dumps(protocols))
    )


def connection_settings(user, password):
    return {
        "connection": {
            "type": "rdp",
            "settings": {
                "hostname": DIAL_HOST,
                "port": DIAL_PORT,
                "username": user,
                "password": password,
                "security": "any",
                "ignore-cert": "true",
                "resize-method": "display-update",
                "enable-wallpaper": "false",
                "width": "1280",
                "height": "720",
            },
        }
    }


def parse_args():
    parser = argparse.ArgumentParser(description="Open an RDP session on the WireGuard peer through the MicroVM.")
    parser.add_argument("--user", default=os.environ.get("GUAC_RDP_USER", ""))
    parser.add_argument("--password", default=os.environ.get("GUAC_RDP_PASSWORD", ""))
    parser.add_argument("--local", action="store_true", help="proxy to a local runner on 127.0.0.1:8081")
    parser.add_argument("--key", default=os.environ.get("GUAC_CRYPT_KEY", ""), help="32-byte guacamole-lite key")
    parser.add_argument("--upstream", default="127.0.0.1:8081", help="host:port of guacamole-lite in --local mode")
    parser.add_argument("--listen", default=LISTEN)
    return parser.parse_args()


def main():
    args = parse_args()
    if not args.user or not args.password:
        raise SystemExit("Set --user and --password, or GUAC_RDP_USER and GUAC_RDP_PASSWORD.")
    host, port_text = args.listen.rsplit(":", 1)
    listen_port = int(port_text)

    if args.local:
        key = args.key
        if not key:
            raise SystemExit("--local needs --key or GUAC_CRYPT_KEY (printed by the local runner).")
        upstream_host, upstream_port = args.upstream.rsplit(":", 1)
        ws_url = f"ws://{upstream_host}:{upstream_port}/websocket"
        protocols = ["guacamole"]
    else:
        state = load_state()
        key = args.key or state.get("guacCryptKey", "")
        if not key:
            raise SystemExit(f"{STATE_FILE} has no guacCryptKey. Run 04_run_microvm.py again.")
        auth_token = create_auth_token(state["microvmId"], state.get("region", AWS_REGION))
        ws_url = f"wss://{state['endpoint']}/websocket"
        protocols = microvm_protocols(auth_token)

    token = encrypt_token(connection_settings(args.user, args.password), key)
    page = render_page(token, ws_url, protocols)

    async def handle_index(request):
        return web.Response(text=page, content_type="text/html")

    app = web.Application()
    app.router.add_get("/", handle_index)

    async def open_browser(app):
        await asyncio.sleep(0.5)
        webbrowser.open(f"http://{host}:{listen_port}/")

    app.on_startup.append(open_browser)
    print(f"RDP page  http://{host}:{listen_port}/")
    print(f"WebSocket {ws_url}")
    if not args.local:
        print("The WireGuard tunnel to 10.200.99.2 must already be up.")
    web.run_app(app, host=host, port=listen_port, print=None)


if __name__ == "__main__":
    main()
