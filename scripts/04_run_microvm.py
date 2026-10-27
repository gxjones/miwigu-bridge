"""
04_run_microvm.py

Runs a MicroVM from the image created in step 03, polls until RUNNING,
and writes the microvmId + endpoint to state.json for subsequent scripts.
"""

import json
import os
import secrets
import sys
import time
import boto3

sys.path.insert(0, ".")
from config import (
    AWS_REGION,
    IMAGE_NAME,
    INGRESS_CONNECTOR_ARN,
    EGRESS_CONNECTOR_ARN,
    IDLE_POLICY,
    STATE_FILE,
)

POLL_INTERVAL_SEC = 5
# States that mean the MicroVM is no longer being provisioned.
TERMINAL_OR_RUNNING = {"RUNNING", "TERMINATING", "TERMINATED"}

WG_HOST_DIR = os.path.join(os.path.dirname(__file__), "..", "miwigu", "wg_host")
# Public key of the Windows dummy tunnel (windows-dummy.conf Interface PrivateKey).
WINDOWS_PEER_PUBLIC_KEY = "GHhBnw7IqG3OcOcjH7lE3xXVOsNLH64SWpofim1wkE8="
# 32-byte AES key for guacamole-lite. Minted here and stored in state.json.
GUAC_CRYPT_KEY = secrets.token_hex(16)


def run_hook_payload() -> str:
    """JSON string Lambda delivers to the guest /run hook."""
    key_path = os.path.join(WG_HOST_DIR, "server.key")
    with open(key_path) as f:
        private = f.read().strip()
    if not private:
        raise SystemExit(f"Server private key is empty: {key_path}")
    return json.dumps(
        {
            "serverPrivateKey": private,
            "peerPublicKey": WINDOWS_PEER_PUBLIC_KEY,
            "hostAddr": "127.0.0.1",
            "guacCryptKey": GUAC_CRYPT_KEY,
        }
    )


def get_image_arn() -> str:
    account_id = boto3.client("sts").get_caller_identity()["Account"]
    return f"arn:aws:lambda:{AWS_REGION}:{account_id}:microvm-image:{IMAGE_NAME}"


def main():
    microvms = boto3.client("lambda-microvms", region_name=AWS_REGION)
    image_arn = get_image_arn()

    print(f"Running MicroVM from image '{IMAGE_NAME}' ...")
    response = microvms.run_microvm(
        imageIdentifier=image_arn,
        ingressNetworkConnectors=[INGRESS_CONNECTOR_ARN],
        egressNetworkConnectors=[EGRESS_CONNECTOR_ARN],
        idlePolicy=IDLE_POLICY,
        runHookPayload=run_hook_payload(),
    )

    microvm_id = response["microvmId"]
    endpoint = response.get("endpoint", "")
    print(f"MicroVM ID : {microvm_id}")
    print(f"Endpoint   : {endpoint}")

    # Write this before polling. The run hook already has the guacamole key,
    # and a later interrupt should not throw that key away.
    state_data = {
        "microvmId": microvm_id,
        "endpoint": endpoint,
        "region": AWS_REGION,
        "guacCryptKey": GUAC_CRYPT_KEY,
    }
    state_path = os.path.join(os.path.dirname(__file__), "..", STATE_FILE)
    with open(state_path, "w") as f:
        json.dump(state_data, f, indent=2)

    print("Polling until RUNNING...\n")

    while True:
        status = microvms.get_microvm(microvmIdentifier=microvm_id)
        state = status.get("state", "UNKNOWN")
        print(f"  [{time.strftime('%H:%M:%S')}] state={state}")

        if state in TERMINAL_OR_RUNNING:
            break

        time.sleep(POLL_INTERVAL_SEC)

    if state != "RUNNING":
        print(f"\nMicroVM ended up in unexpected state: {state}")
        sys.exit(1)

    print(f"\nMicroVM is RUNNING.")
    print(f"Endpoint : https://{endpoint}/")
    print(f"State    : written to {STATE_FILE}")
    print("Guacamole key is in that file. scripts/08_guacamole.py reads it.")


if __name__ == "__main__":
    main()
