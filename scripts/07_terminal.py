"""
07_terminal.py

Starts a local aiohttp proxy that:
  - Serves an xterm.js web terminal at http://localhost:8765/
  - Proxies WebSocket connections to the MicroVM at wss://{endpoint}/ws,
    injecting the AWS auth token header that browsers cannot set themselves.

Usage:
  python scripts/07_terminal.py

Requires a running MicroVM (state.json from 04_run_microvm.py).
The browser will open automatically.
"""

import asyncio
import json
import os
import sys
import webbrowser

import boto3
import aiohttp
from aiohttp import web
from aiohttp.client_reqrep import CIMultiDict

sys.path.insert(0, ".")
from config import AWS_REGION, STATE_FILE

LOCAL_PORT = 8765
TOKEN_EXPIRY_MINUTES = 60

MICROVM_PORT = 8080

TERMINAL_HTML = """\
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Lambda MicroVM Terminal</title>
  <link
    rel="stylesheet"
    href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.min.css"
  />
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    html, body { height: 100%; background: #1e1e1e; }
    #terminal-container {
      height: 100%;
      padding: 8px;
    }
  </style>
</head>
<body>
  <div id="terminal-container"></div>

  <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
  <script>
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: "'Cascadia Code', 'Fira Code', 'Menlo', monospace",
      theme: {
        background: '#1e1e1e',
        foreground: '#d4d4d4',
        cursor:     '#d4d4d4',
        black:      '#1e1e1e',
        red:        '#f44747',
        green:      '#6a9955',
        yellow:     '#d7ba7d',
        blue:       '#569cd6',
        magenta:    '#c586c0',
        cyan:       '#9cdcfe',
        white:      '#d4d4d4',
        brightBlack:   '#808080',
        brightRed:     '#f44747',
        brightGreen:   '#6a9955',
        brightYellow:  '#d7ba7d',
        brightBlue:    '#569cd6',
        brightMagenta: '#c586c0',
        brightCyan:    '#9cdcfe',
        brightWhite:   '#ffffff',
      },
    });

    const fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('terminal-container'));
    fitAddon.fit();

    const wsUrl = `ws://${location.host}/ws`;
    const ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';

    ws.addEventListener('open', () => {
      term.writeln('\\x1b[32mConnected to Lambda MicroVM.\\x1b[0m');

      // Send initial terminal size
      sendResize();

      // Forward keypresses to the MicroVM shell
      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(data);
        }
      });
    });

    ws.addEventListener('message', (event) => {
      if (event.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(event.data));
      } else {
        term.write(event.data);
      }
    });

    ws.addEventListener('close', () => {
      term.writeln('\\r\\n\\x1b[31mConnection closed.\\x1b[0m');
    });

    ws.addEventListener('error', () => {
      term.writeln('\\r\\n\\x1b[31mWebSocket error — check proxy logs.\\x1b[0m');
    });

    function sendResize() {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    }

    window.addEventListener('resize', () => {
      fitAddon.fit();
      sendResize();
    });
  </script>
</body>
</html>
"""


def load_state() -> dict:
    state_path = os.path.join(os.path.dirname(__file__), "..", STATE_FILE)
    if not os.path.exists(state_path):
        print(f"Error: {STATE_FILE} not found. Run 04_run_microvm.py first.")
        sys.exit(1)
    with open(state_path) as f:
        return json.load(f)


def create_auth_token(microvm_id: str, region: str) -> dict:
    microvms = boto3.client("lambda-microvms", region_name=region)
    print(f"Creating auth token for MicroVM {microvm_id} ...")
    response = microvms.create_microvm_auth_token(
        microvmIdentifier=microvm_id,
        expirationInMinutes=TOKEN_EXPIRY_MINUTES,
        allowedPorts=[{"allPorts": {}}],
    )
    auth_token = response["authToken"]
    # authToken is a dict of header name → value
    return auth_token if isinstance(auth_token, dict) else {"X-aws-proxy-auth": auth_token}


async def handle_index(request):
    return web.Response(text=TERMINAL_HTML, content_type="text/html")


async def handle_ws(request):
    """
    Accepts a WebSocket from the browser, then opens an upstream WebSocket
    to the MicroVM (with the AWS auth header injected), and bidirectionally
    proxies messages between the two.
    """
    app_state = request.app["state"]
    endpoint = app_state["endpoint"]
    auth_headers = app_state["auth_headers"]

    upstream_url = f"wss://{endpoint}/ws"

    # heartbeat sends a WebSocket ping every N seconds and closes the connection
    # if no pong is received, keeping both the browser and AWS proxy legs alive.
    browser_ws = web.WebSocketResponse(heartbeat=20.0)
    await browser_ws.prepare(request)

    # Build upstream headers: auth token + port routing header.
    # X-aws-proxy-port is the documented header-based port routing mechanism;
    # the subprotocol alternative is only needed when headers can't be set (browsers).
    upstream_headers = CIMultiDict(auth_headers)
    upstream_headers["X-aws-proxy-port"] = str(MICROVM_PORT)

    session = request.app["session"]
    try:
        async with session.ws_connect(
            upstream_url,
            headers=upstream_headers,
            ssl=True,
            heartbeat=20.0,
        ) as microvm_ws:

            async def browser_to_microvm():
                async for msg in browser_ws:
                    if msg.type == aiohttp.WSMsgType.TEXT:
                        await microvm_ws.send_str(msg.data)
                    elif msg.type == aiohttp.WSMsgType.BINARY:
                        await microvm_ws.send_bytes(msg.data)
                    elif msg.type in (aiohttp.WSMsgType.CLOSE, aiohttp.WSMsgType.ERROR):
                        break

            async def microvm_to_browser():
                async for msg in microvm_ws:
                    if msg.type == aiohttp.WSMsgType.TEXT:
                        await browser_ws.send_str(msg.data)
                    elif msg.type == aiohttp.WSMsgType.BINARY:
                        await browser_ws.send_bytes(msg.data)
                    elif msg.type in (aiohttp.WSMsgType.CLOSE, aiohttp.WSMsgType.ERROR):
                        break

            # Run both directions concurrently; stop when either side closes
            done, pending = await asyncio.wait(
                [
                    asyncio.ensure_future(browser_to_microvm()),
                    asyncio.ensure_future(microvm_to_browser()),
                ],
                return_when=asyncio.FIRST_COMPLETED,
            )
            for task in pending:
                task.cancel()

    except aiohttp.ClientError as e:
        print(f"Upstream connection error: {e}")

    await browser_ws.close()
    return browser_ws


async def on_startup(app):
    app["session"] = aiohttp.ClientSession()


async def on_cleanup(app):
    await app["session"].close()


def main():
    state = load_state()
    microvm_id = state["microvmId"]
    endpoint = state["endpoint"]
    region = state.get("region", AWS_REGION)

    auth_headers = create_auth_token(microvm_id, region)
    print(f"Token created (expires in {TOKEN_EXPIRY_MINUTES} min).")
    print(f"MicroVM endpoint : https://{endpoint}/")
    print(f"Local terminal   : http://localhost:{LOCAL_PORT}/")

    app = web.Application()
    app["state"] = {"endpoint": endpoint, "auth_headers": auth_headers}

    app.on_startup.append(on_startup)
    app.on_cleanup.append(on_cleanup)

    app.router.add_get("/", handle_index)
    app.router.add_get("/ws", handle_ws)

    # Open browser after a short delay to let the server start
    async def open_browser(app):
        await asyncio.sleep(0.5)
        webbrowser.open(f"http://localhost:{LOCAL_PORT}/")

    app.on_startup.append(open_browser)

    print("\nStarting local proxy. Press Ctrl-C to stop.\n")
    web.run_app(app, host="127.0.0.1", port=LOCAL_PORT, print=None)


if __name__ == "__main__":
    main()
