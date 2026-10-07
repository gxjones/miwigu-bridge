#!/bin/sh
# Bring up the same peer as windows-dummy.conf and send WireGuard to wstoudp.
# Defaults match that file: client 10.200.99.2, server 10.200.99.1, UDP 51820.
set -eu

ENDPOINT_HOST="${WG_ENDPOINT_HOST:-host.docker.internal}"
ENDPOINT_PORT="${WG_ENDPOINT_PORT:-51820}"
PRIVATE_KEY="${WG_PRIVATE_KEY:?set WG_PRIVATE_KEY}"
PEER_PUBLIC="${WG_PEER_PUBLIC:-mWbVAoXHBA73K0xxqyK8zJW62nzG4J3cx8uUhebulk0=}"
ADDRESS="${WG_ADDRESS:-10.200.99.2/32}"
ALLOWED_IPS="${WG_ALLOWED_IPS:-10.200.99.1/32}"
MTU="${WG_MTU:-1300}"
PROBE_URL="${WG_PROBE_URL:-http://10.200.99.1/}"

umask 077
mkdir -p /etc/wireguard
printf '%s\n' "$PRIVATE_KEY" > /etc/wireguard/private.key

cleanup() {
	ip link delete wg0 2>/dev/null || true
}
trap cleanup EXIT INT TERM

ip link add dev wg0 type wireguard
wg set wg0 private-key /etc/wireguard/private.key
wg set wg0 peer "$PEER_PUBLIC" \
	endpoint "${ENDPOINT_HOST}:${ENDPOINT_PORT}" \
	allowed-ips "$ALLOWED_IPS" \
	persistent-keepalive 25
ip address add "$ADDRESS" dev wg0
ip link set mtu "$MTU" up dev wg0
ip route replace "$ALLOWED_IPS" dev wg0

echo "wireguard up  ${ADDRESS} -> ${ENDPOINT_HOST}:${ENDPOINT_PORT}"
wg show

if curl -fsS --max-time 15 "$PROBE_URL"; then
	echo "probe ok  ${PROBE_URL}"
else
	echo "probe failed  ${PROBE_URL}; tunnel stays up" >&2
fi

while true; do
	sleep 3600
done
