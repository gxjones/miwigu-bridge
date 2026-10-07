# Guest image for a Graviton Lambda MicroVM.
# Lambda itself is the public WebSocket. This process is what it forwards to.
#
# Build context is this directory. The aarch64 libraries must already exist:
#   ./build-aarch64.sh
#
# Keys are not copied into the image. Pass them to run-microvm as the /run
# payload: {"serverPrivateKey":"...","peerPublicKey":"...","hostAddr":"127.0.0.1"}
FROM public.ecr.aws/lambda/microvms:al2023-minimal

RUN dnf install -y python3 python3-pip libgcc \
    && python3 -m pip install --no-cache-dir asyncssh \
    && dnf clean all

WORKDIR /opt/wg_host
COPY wg_host_listener.py success_server.py ssh_server.py start.sh .
COPY dist/aarch64-unknown-linux-gnu/libtunn_ffi.so dist/aarch64-unknown-linux-gnu/liblwip_port.so lib/
RUN chmod +x start.sh

ENV WG_HOST_MODE=microvm \
    TUNN_LIB=/opt/wg_host/lib/libtunn_ffi.so \
    LWIP_LIB=/opt/wg_host/lib/liblwip_port.so \
    WG_HOST_ADDR=127.0.0.1 \
    WG_HOST_LOG=/tmp/wg-host-listener.log

EXPOSE 8080 9000
CMD ["/opt/wg_host/start.sh"]
