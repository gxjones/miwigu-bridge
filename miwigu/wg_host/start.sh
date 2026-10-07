#!/bin/sh
# Ports 80 and 22 are the tunnel's local targets. The listener stays in the foreground.
python3 /opt/wg_host/success_server.py &
python3 /opt/wg_host/ssh_server.py &
exec python3 /opt/wg_host/wg_host_listener.py
