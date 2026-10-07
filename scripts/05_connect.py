"""
05_connect.py

Creates a short-lived auth token for the running MicroVM and prints a
wstoudp command for that endpoint.
"""

import json
import os
import sys
import boto3
import requests

sys.path.insert(0, ".")
from config import AWS_REGION, STATE_FILE

TOKEN_EXPIRY_MINUTES = 30


def load_state() -> dict:
    state_path = os.path.join(os.path.dirname(__file__), "..", STATE_FILE)
    if not os.path.exists(state_path):
        print(f"Error: {STATE_FILE} not found. Run 04_run_microvm.py first.")
        sys.exit(1)
    with open(state_path) as f:
        return json.load(f)


def main():
    state = load_state()
    microvm_id = state["microvmId"]
    endpoint = state["endpoint"]
    region = state.get("region", AWS_REGION)

    microvms = boto3.client("lambda-microvms", region_name=region)

    print(f"Creating auth token for MicroVM {microvm_id} ...")
    token_response = microvms.create_microvm_auth_token(
        microvmIdentifier=microvm_id,
        expirationInMinutes=TOKEN_EXPIRY_MINUTES,
        allowedPorts=[{"allPorts": {}}],
    )
    auth_token = token_response["authToken"]["X-aws-proxy-auth"]
    print(f"./wstoudp -url wss://{endpoint}/ -token {auth_token}")
    # # authToken is already a dict of header name → value (e.g. {"X-aws-proxy-auth": "..."})
    # auth_headers = auth_token if isinstance(auth_token, dict) else {"X-aws-proxy-auth": auth_token}
    # print(f"Token created (expires in {TOKEN_EXPIRY_MINUTES} min).\n")

    # url = f"https://{endpoint}/"
    # print(f"GET {url}")
    # resp = requests.get(url, headers=auth_headers, timeout=30)

    # print(f"HTTP {resp.status_code}")
    # try:
    #     print(json.dumps(resp.json(), indent=2))
    # except ValueError:
    #     print(resp.text)


if __name__ == "__main__":
    main()
