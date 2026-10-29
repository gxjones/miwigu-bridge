# Local runner for the Windows WebSocket proxy (wstoudp).
#
# Builds BoringTun and lwIP, then runs the same guest listener the MicroVM
# uses. A front door on port 8080 accepts the Lambda MicroVM subprotocols and
# forwards the socket to that listener. Decrypted TCP is handed to the
# success server on port 80 and the SSH server on port 22 inside the container.
#
#   docker build -f local/Dockerfile -t local-runner .
#   docker run --rm -p 8080:8080 -p 8081:8081 local-runner
#
# The listener prints a WireGuard client config. Its Endpoint is
# 127.0.0.1:51820, which is wstoudp. Point the proxy at ws://<this-host>:8080/
# with any non-empty token.
#
# To reuse an existing pair instead of generating one:
#   docker run --rm -p 8080:8080 -p 8081:8081 \
#     -v "$PWD/miwigu/wg_host/server.key:/opt/wg_host/server.key:ro" \
#     -v "$PWD/miwigu/wg_host/windows-dummy.conf:/opt/wg_host/windows-dummy.conf:ro" \
#     local-runner
FROM rust:1-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY miwigu/wg_host/tunn_ffi tunn_ffi
COPY miwigu/wg_host/lwip_port lwip_port
COPY miwigu/wg_host/third_party/lwip-STABLE-2_2_1_RELEASE third_party/lwip-STABLE-2_2_1_RELEASE

RUN cargo build --release --manifest-path tunn_ffi/Cargo.toml
RUN make -C lwip_port OUT=/src/liblwip_port.so CC=gcc

FROM python:3.12-slim-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends libgcc-s1 wireguard-tools nodejs npm ca-certificates \
    && python3 -m pip install --no-cache-dir asyncssh \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/wg_host
COPY miwigu/wg_host/wg_host_listener.py miwigu/wg_host/success_server.py miwigu/wg_host/ssh_server.py ./
COPY miwigu/wg_host/install-guacd.sh ./
COPY miwigu/wg_host/patches/ patches/
COPY miwigu/wg_host/guac/ guac/
COPY local/front.py local/entrypoint.sh ./
COPY --from=build /src/tunn_ffi/target/release/libtunn_ffi.so /src/liblwip_port.so lib/
RUN chmod +x entrypoint.sh install-guacd.sh \
    && ./install-guacd.sh \
    && npm ci --omit=dev --prefix /opt/wg_host/guac \
    && sed -i 's/this.lastActivity + 10000/this.lastActivity + 900000/' \
        /opt/wg_host/guac/node_modules/guacamole-lite/lib/GuacdClient.js

ENV PYTHONUNBUFFERED=1 \
    GUAC_CRYPT_FILE=/tmp/guac-crypt.key
EXPOSE 8080 8081
ENTRYPOINT ["/opt/wg_host/entrypoint.sh"]
