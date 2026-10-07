"""
06_terminate.py

Terminates the running MicroVM identified in state.json and removes the file.
"""

import json
import os
import sys
import boto3

sys.path.insert(0, ".")
from config import AWS_REGION, STATE_FILE


def load_state() -> dict:
    state_path = os.path.join(os.path.dirname(__file__), "..", STATE_FILE)
    if not os.path.exists(state_path):
        print(f"Error: {STATE_FILE} not found. Nothing to terminate.")
        sys.exit(1)
    with open(state_path) as f:
        return json.load(f), state_path


def main():
    state, state_path = load_state()
    microvm_id = state["microvmId"]
    region = state.get("region", AWS_REGION)

    microvms = boto3.client("lambda-microvms", region_name=region)

    print(f"Terminating MicroVM {microvm_id} ...")
    microvms.terminate_microvm(microvmIdentifier=microvm_id)
    print("Terminate request sent.")

    os.remove(state_path)
    print(f"Removed {STATE_FILE}.")
    print("\nDone. All resources have been released.")


if __name__ == "__main__":
    main()
