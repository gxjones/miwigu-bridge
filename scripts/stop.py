#!/usr/bin/env python3
"""
stop.py

Terminate every MicroVM started by scripts/spawn.py.

    python scripts/stop.py
    python scripts/stop.py --aws

Reads spawned.json. Also stops the MicroVM in state.json when that file is
from a spawn that predates the ledger. --aws asks AWS for MicroVMs in this
region that are still pending, running, suspending, or suspended, and stops
those too. When a t6 uid was recorded and USER_API_KEY is set (environment
or .env), DELETEs that VM request too.
"""

import argparse
import json
import os
import sys
from urllib.parse import quote

import boto3
import requests
from botocore.exceptions import ClientError

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
LEDGER = os.path.join(ROOT, "spawned.json")
T6 = "https://t6.staticlightlabs.com"
GONE = {"ResourceNotFoundException", "NotFoundException"}
# Still allocated. TERMINATING is already shutting down, TERMINATED is gone.
LIVE = {"PENDING", "RUNNING", "SUSPENDING", "SUSPENDED"}


def load_env(path):
    if not os.path.exists(path):
        return
    with open(path) as fh:
        for raw in fh:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[len("export "):].strip()
            key, sep, value = line.partition("=")
            if not sep:
                continue
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            os.environ[key.strip()] = value


def load_json(path):
    with open(path) as fh:
        return json.load(fh)


def load_ledger():
    if not os.path.exists(LEDGER):
        return []
    items = load_json(LEDGER)
    return items if isinstance(items, list) else []


def save_ledger(items):
    with open(LEDGER, "w") as fh:
        json.dump(items, fh, indent=2)
        fh.write("\n")


def delete_request(api_key, uid):
    response = requests.delete(
        f"{T6}/api/user/vm/{quote(uid, safe='')}",
        headers={"Authorization": f"Bearer {api_key}"},
        timeout=60,
    )
    if not response.ok:
        print(f"t6 delete failed for {uid}: HTTP {response.status_code} {response.text}", file=sys.stderr)
        return False
    print(f"Deleted t6 request {uid}: {response.text}")
    return True


def list_live_microvms(region):
    client = boto3.client("lambda-microvms", region_name=region)
    found = []
    token = None
    while True:
        kwargs = {"maxResults": 50}
        if token:
            kwargs["nextToken"] = token
        page = client.list_microvms(**kwargs)
        for item in page.get("items", []):
            if item.get("state") in LIVE:
                found.append(item)
        token = page.get("nextToken")
        if not token:
            return found


def terminate(microvm_id, region):
    print(f"Terminating MicroVM {microvm_id} ...")
    try:
        boto3.client("lambda-microvms", region_name=region).terminate_microvm(
            microvmIdentifier=microvm_id
        )
    except ClientError as exc:
        error = exc.response.get("Error", {})
        code = error.get("Code", "")
        message = error.get("Message", str(exc))
        if code in GONE:
            print(f"Already gone: {message}")
            return True
        print(f"Terminate failed: {code} {message}", file=sys.stderr)
        return False
    print("Terminate request sent.")
    return True


def parse_args():
    parser = argparse.ArgumentParser(description="Terminate MicroVMs started by scripts/spawn.py.")
    parser.add_argument(
        "--aws",
        action="store_true",
        help="Also terminate MicroVMs AWS lists as pending, running, suspending, or suspended.",
    )
    return parser.parse_args()


def main():
    args = parse_args()
    os.chdir(ROOT)
    sys.path.insert(0, ROOT)
    load_env(os.path.join(ROOT, ".env"))

    from config import AWS_REGION, STATE_FILE

    items = load_ledger()
    state_path = os.path.join(ROOT, STATE_FILE)
    if os.path.exists(state_path):
        state = load_json(state_path)
        microvm_id = state.get("microvmId")
        if microvm_id and not any(item.get("microvmId") == microvm_id for item in items):
            items.append({
                "microvmId": microvm_id,
                "region": state.get("region", AWS_REGION),
                "uid": "",
            })

    if args.aws:
        live = list_live_microvms(AWS_REGION)
        print(f"AWS lists {len(live)} MicroVM(s) in {AWS_REGION}.")
        known = {item.get("microvmId") for item in items}
        for vm in live:
            microvm_id = vm["microvmId"]
            vm_state = vm.get("state", "")
            if microvm_id in known:
                print(f"  {microvm_id}  {vm_state}")
                continue
            print(f"  {microvm_id}  {vm_state}  (not in spawned.json)")
            items.append({"microvmId": microvm_id, "region": AWS_REGION, "uid": ""})

    if not items:
        if args.aws:
            print(f"No MicroVMs to stop in {AWS_REGION}.")
        else:
            print("No MicroVMs recorded by spawn.py.")
        return

    api_key = os.environ.get("USER_API_KEY", "")
    remaining = []
    terminated = set()
    failed = False

    for item in items:
        microvm_id = item.get("microvmId") or ""
        region = item.get("region") or AWS_REGION
        uid = item.get("uid") or ""
        vm_done = not microvm_id
        uid_done = not uid

        if microvm_id:
            vm_done = terminate(microvm_id, region)
            if vm_done:
                terminated.add(microvm_id)
            else:
                failed = True

        if vm_done and uid:
            if not api_key:
                print(f"USER_API_KEY is not set; left t6 request {uid}", file=sys.stderr)
                failed = True
            elif delete_request(api_key, uid):
                uid_done = True
            else:
                failed = True

        if not vm_done or not uid_done:
            remaining.append(item)

    if remaining:
        save_ledger(remaining)
    elif os.path.exists(LEDGER):
        os.remove(LEDGER)
        print("Removed spawned.json.")

    if os.path.exists(state_path):
        state = load_json(state_path)
        if state.get("microvmId") in terminated:
            os.remove(state_path)
            print(f"Removed {STATE_FILE}.")

    if failed:
        sys.exit(1)
    print("Done.")


if __name__ == "__main__":
    main()
