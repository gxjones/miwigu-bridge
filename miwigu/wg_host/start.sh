#!/bin/sh
# Ports 80 and 22 are the tunnel's local targets. guacd dials 127.0.0.1:3389,
# which lwIP forwards to the WireGuard peer. The listener stays in the foreground.
python3 /opt/wg_host/success_server.py &
python3 /opt/wg_host/ssh_server.py &
# Debug goes to a file the port 80 server can hand back. CloudWatch does not
# keep the running guest's stderr.
/usr/local/sbin/guacd -L debug -b 127.0.0.1 -l 4822 -f >>/tmp/guacd.log 2>&1 &
# guacamole-lite 1.2.0 throws on a connection with a bad or missing token and
# takes the process down, so keep restarting it.
(
    while true; do
        node /opt/wg_host/guac/server.js
        echo "guacamole-lite exited $?, restarting"
        sleep 1
    done
) >>/tmp/guacd.log 2>&1 &
exec python3 /opt/wg_host/wg_host_listener.py
