#!/bin/sh
# Rebuilds the guest image from the working tree and starts a fresh MicroVM.
#
#   scripts/redeploy.sh
#
# Steps: terminate the MicroVM in state.json (if any), upload app.zip, rebuild
# the image with --force, run a new MicroVM, and print the tunnel command.
# If GUAC_RDP_USER and GUAC_RDP_PASSWORD are set, it then starts the RDP page.
set -eu

cd "$(dirname "$0")/.."

if [ -f .env ]; then
    set -a
    . ./.env
    set +a
fi

if [ -x .venv/bin/python ]; then
    PY=.venv/bin/python
else
    PY=python3
fi

# A MicroVM that idled out is already gone, and terminating it again fails.
if [ -f state.json ]; then
    "$PY" scripts/06_terminate.py || rm -f state.json
fi
"$PY" scripts/02_upload_artifact.py
"$PY" scripts/03_create_image.py --force
"$PY" scripts/04_run_microvm.py
"$PY" scripts/05_connect.py

if [ -n "${GUAC_RDP_USER:-}" ] && [ -n "${GUAC_RDP_PASSWORD:-}" ]; then
    echo
    echo "Bring the tunnel up with the command above, then use the RDP page."
    exec "$PY" scripts/08_guacamole.py
fi
