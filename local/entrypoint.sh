#!/bin/sh
# Local runner: BoringTun + lwIP behind the same WebSocket the proxy dials.
set -eu
cd /opt/wg_host

cleanup() {
    kill $(jobs -p) 2>/dev/null || true
}
trap cleanup INT TERM

umask 077
if [ ! -f server.key ] && [ ! -f windows-dummy.conf ]; then
    server_private=$(wg genkey)
    peer_private=$(wg genkey)
    printf '%s\n' "$server_private" > server.key
    printf '%s\n' "[Interface]" "PrivateKey = $peer_private" > windows-dummy.conf
    echo "generated a new WireGuard keypair for this container"
elif [ ! -f server.key ] || [ ! -f windows-dummy.conf ]; then
    echo "mount both server.key and windows-dummy.conf, or neither" >&2
    exit 1
fi

peer_private=$(sed -n 's/^PrivateKey[[:space:]]*=[[:space:]]*//p' windows-dummy.conf | head -n 1)
if [ -z "$peer_private" ]; then
    echo "windows-dummy.conf is missing Interface PrivateKey" >&2
    exit 1
fi
export WG_PEER_PUBLIC=$(printf '%s\n' "$peer_private" | wg pubkey)

if [ -z "${GUAC_CRYPT_KEY:-}" ]; then
    GUAC_CRYPT_KEY=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
fi
export GUAC_CRYPT_KEY
printf '%s' "$GUAC_CRYPT_KEY" > /tmp/guac-crypt.key
chmod 600 /tmp/guac-crypt.key

export WG_LISTEN_HOST=127.0.0.1
export WG_LISTEN_PORT=18080
export TUNN_LIB=/opt/wg_host/lib/libtunn_ffi.so
export LWIP_LIB=/opt/wg_host/lib/liblwip_port.so
export WG_HOST_ADDR=127.0.0.1
export WG_HOST_LOG=/tmp/wg-host-listener.log
export PYTHONUNBUFFERED=1

python3 success_server.py &
python3 ssh_server.py &
/usr/local/sbin/guacd -b 127.0.0.1 -l 4822 -f &
node /opt/wg_host/guac/server.js &
python3 wg_host_listener.py &
listener=$!

ready=0
i=0
while [ "$i" -lt 50 ]; do
    if ! kill -0 "$listener" 2>/dev/null; then
        echo "wireguard listener exited" >&2
        cat "$WG_HOST_LOG" >&2 || true
        cleanup
        exit 1
    fi
    if grep -q "listening  ws://" "$WG_HOST_LOG" 2>/dev/null; then
        ready=1
        break
    fi
    i=$((i + 1))
    sleep 0.1
done
if [ "$ready" -ne 1 ]; then
    echo "wireguard listener did not become ready" >&2
    cleanup
    exit 1
fi

echo "guac crypt key: $GUAC_CRYPT_KEY"
echo "guacamole-lite on port 8081"

python3 front.py &
front=$!
wait "$front" || status=$?
cleanup
exit "${status:-0}"
