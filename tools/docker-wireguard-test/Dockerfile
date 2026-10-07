# Linux WireGuard client. Its peer endpoint is wstoudp's UDP port on the host.
#
# wstoudp binds loopback unless told otherwise, and Docker cannot send to that.
# Start it on all interfaces first:
#   ./wstoudp -listen 0.0.0.0:51820 -url wss://... -token ...
#
#   docker build -t wg-test tools/docker-wireguard-test
#   docker run --rm --cap-add NET_ADMIN --device /dev/net/tun \
#     --add-host=host.docker.internal:host-gateway \
#     wg-test
FROM alpine:3.22

RUN apk add --no-cache wireguard-tools iproute2 curl

COPY run.sh /run.sh
RUN chmod +x /run.sh

ENTRYPOINT ["/run.sh"]
