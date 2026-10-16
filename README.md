# miwigu

WireGuard over an [AWS Lambda MicroVM](https://docs.aws.amazon.com/lambda/latest/dg/lambda-microvms-guide.html) WebSocket.

A MicroVM only accepts authenticated HTTPS and WebSocket traffic. WireGuard speaks UDP, so a client cannot use the MicroVM endpoint as its `Endpoint`. miwigu carries the tunnel over that WebSocket: a proxy beside the WireGuard client, and a guest inside the MicroVM that terminates the tunnel and answers on ordinary TCP.

```
WireGuard client                 10.200.99.2
    UDP 127.0.0.1:51820
        │
        ▼
wstoudp                          local proxy
    one datagram per WebSocket frame, plus the Lambda auth token
        │
        ▼
Lambda MicroVM proxy
        │
        ▼
wg_host                          guest, 10.200.99.1
    BoringTun → lwIP → TCP
        ├── :80   HTTP, body "success"
        └── :22   SSH, prints "success"
```

The client's WireGuard config points at `127.0.0.1:51820`, which is `wstoudp`.

- [How MicroVMs work](https://docs.aws.amazon.com/lambda/latest/dg/microvms-how-it-works.html)
- [Getting started](https://docs.aws.amazon.com/lambda/latest/dg/microvms-getting-started.html)

## Layout

```
├── config.py                 # S3 bucket, region, image name, idle policy
├── Makefile                  # wstoudp binaries, guest libraries, app.zip
├── miwigu/
│   ├── wstoudp/              # WireGuard UDP ↔ WebSocket
│   └── wg_host/              # guest: WebSocket → BoringTun → lwIP → TCP
├── scripts/                  # create the image, run it, print a wstoudp command
├── local/                    # same guest, in Docker, for a local wstoudp
└── tools/docker-wireguard-test/   # Linux WireGuard client aimed at wstoudp
```

## wstoudp

`miwigu/wstoudp` listens on `127.0.0.1:51820`. Each WireGuard UDP datagram becomes one WebSocket binary message, and each reply is written back to the client.

```bash
./wstoudp -url wss://<endpoint>/ -token <token>
```

`-listen` changes the UDP address. Ctrl-C stops the proxy; the WireGuard client keeps using `127.0.0.1:51820`.

`make wstoudp` builds it into `bin/<os>-<arch>/`. Linux and macOS get a command-line program. The Windows build is a small GUI (`wstoudp.exe`) and needs `wstoudp.exe.manifest` beside it.

## wg_host

`miwigu/wg_host` is the guest image. Lambda forwards the WebSocket to `wg_host_listener.py` on port 8080. BoringTun (`tunn_ffi`) decrypts each frame, and lwIP (`lwip_port`) turns the inner packets into TCP connections to the two processes `start.sh` starts:

- port 80 — an HTTP server that returns `success`
- port 22 — an SSH server that prints `success` and exits

`http://10.200.99.1/` or an SSH session to `10.200.99.1` is how you tell the tunnel is up.

The WireGuard private key is not copied into the image. `scripts/04_run_microvm.py` reads `miwigu/wg_host/server.key` (not committed) and sends it in the run-hook payload. The listener creates the tunnel only after `POST /aws/lambda-microvms/runtime/v1/run`, so the key is not captured in the image snapshot. The accepted peer is the public key of the Windows dummy tunnel, hardcoded in that script.

Prebuilt aarch64 libraries are in `miwigu/wg_host/dist/`. Rebuild them on Linux with `make libs`, or with `miwigu/wg_host/build-aarch64.sh`. That needs `gcc` and `aarch64-linux-gnu-gcc`.

## Build

```bash
make wstoudp     # bin/<os>-<arch>/wstoudp
make libs        # BoringTun and lwIP .so files for aarch64 and x86_64 (Linux)
make artifact    # app.zip, the guest Docker context scripts/02 uploads
make check
```

`make libs` cross-compiles. On macOS, `make wstoudp` and `make artifact` are enough when the committed `dist/` libraries are still good.

## Run it on AWS

1. An S3 bucket in the target region.
2. AWS credentials that can create IAM roles and write that bucket.
3. `lambda-microvms` available in that region.
4. `miwigu/wg_host/server.key`, the guest private key.

```bash
pip install -r requirements.txt
```

Set `S3_BUCKET` and `AWS_REGION` in `config.py`, or export `MICROVM_S3_BUCKET` and `AWS_DEFAULT_REGION`.

From the repo root:

```bash
python scripts/01_setup_iam.py        # IAM build role; safe to repeat
python scripts/02_upload_artifact.py  # zip miwigu/wg_host and upload it
python scripts/03_create_image.py     # build the MicroVM image
python scripts/04_run_microvm.py      # launch it; writes state.json
python scripts/05_connect.py          # print: ./wstoudp -url wss://... -token ...
python scripts/06_terminate.py        # terminate and delete state.json
```

Script 04 reads `server.key` and waits until the MicroVM is `RUNNING`. Script 05 prints a `wstoudp` command with a fresh auth token. Start `wstoudp` with that command, bring up a WireGuard peer for `10.200.99.2` whose endpoint is `127.0.0.1:51820`, then request `http://10.200.99.1/`.

`config.py` suspends the MicroVM after 15 minutes idle and resumes it when traffic arrives. A MicroVM that stays suspended for 5 minutes is terminated.

```
PENDING → RUNNING → SUSPENDING → SUSPENDED
                ↑_________________________↑
RUNNING / SUSPENDED → TERMINATING → TERMINATED
```

## Try it locally

`local/Dockerfile` builds the same guest and serves it on port 8080, with a front door that accepts the MicroVM WebSocket subprotocols. Any non-empty token works.

```bash
docker build -f local/Dockerfile -t local-runner .
docker run --rm -p 8080:8080 local-runner
```

The listener prints a WireGuard client config. Point `wstoudp` at `ws://127.0.0.1:8080/` and use that config, whose endpoint is `127.0.0.1:51820`.

`tools/docker-wireguard-test` is a Linux WireGuard client whose peer endpoint is `wstoudp` on the host. Start `wstoudp` on `0.0.0.0:51820` first; Docker cannot send to loopback.

```bash
./wstoudp -listen 0.0.0.0:51820 -url ws://127.0.0.1:8080/ -token local
docker build -t wg-test tools/docker-wireguard-test
docker run --rm --cap-add NET_ADMIN --device /dev/net/tun \
  --add-host=host.docker.internal:host-gateway \
  -e WG_PRIVATE_KEY="<client private key>" \
  wg-test
```
