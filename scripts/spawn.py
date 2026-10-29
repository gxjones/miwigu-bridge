#!/usr/bin/env python3
"""
spawn.py

Start a MicroVM, queue a Windows VM, and publish its desktop.

    python scripts/spawn.py

USER_API_KEY queues the VM and publishes the desktop.
GUAC_RDP_USER and GUAC_RDP_PASSWORD are the desktop connection.
All three may be set in the environment or .env.

Each MicroVM is appended to spawned.json. python scripts/stop.py terminates them.
"""

import importlib.util
import json
import os
import sys
from urllib.parse import quote, urlencode

import boto3
import requests

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
LEDGER = os.path.join(ROOT, "spawned.json")
T6 = "https://t6.staticlightlabs.com"


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


def record_microvm(microvm_id, region):
    items = load_ledger()
    if not any(item.get("microvmId") == microvm_id for item in items):
        items.append({"microvmId": microvm_id, "region": region, "uid": ""})
        save_ledger(items)
    print(f"Recorded {microvm_id} in spawned.json")


def set_uid(microvm_id, uid):
    items = load_ledger()
    for item in items:
        if item.get("microvmId") == microvm_id:
            item["uid"] = uid
    save_ledger(items)


def t6(method, path, api_key, body):
    response = requests.request(
        method,
        T6 + path,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        json=body,
        timeout=60,
    )
    if not response.ok:
        raise SystemExit(f"{method} {path} failed: HTTP {response.status_code}\n{response.text}")
    return response.json()


def create_auth_token(microvm_id, region, minutes):
    print(f"Creating auth token for MicroVM {microvm_id} ...")
    response = boto3.client("lambda-microvms", region_name=region).create_microvm_auth_token(
        microvmIdentifier=microvm_id,
        expirationInMinutes=minutes,
        allowedPorts=[{"allPorts": {}}],
    )
    token = response["authToken"]
    if isinstance(token, dict):
        token = token["X-aws-proxy-auth"]
    return token


def run_microvm():
    path = os.path.join(os.path.dirname(__file__), "04_run_microvm.py")
    spec = importlib.util.spec_from_file_location("run_microvm", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.main()


def main():
    os.chdir(ROOT)
    sys.path.insert(0, ROOT)
    load_env(os.path.join(ROOT, ".env"))

    api_key = os.environ.get("USER_API_KEY", "")
    user = os.environ.get("GUAC_RDP_USER", "")
    password = os.environ.get("GUAC_RDP_PASSWORD", "")
    if not api_key:
        raise SystemExit("USER_API_KEY is not set")
    if not user or not password:
        raise SystemExit("GUAC_RDP_USER and GUAC_RDP_PASSWORD are required")

    from config import AWS_REGION, STATE_FILE

    run_microvm()

    state = load_json(os.path.join(ROOT, STATE_FILE))
    microvm_id = state["microvmId"]
    region = state.get("region", AWS_REGION)
    url = f"wss://{state['endpoint']}/"
    key = state.get("guacCryptKey", "")
    if key and len(key.encode("utf-8")) != 32:
        raise SystemExit("guacCryptKey must be 32 bytes")

    record_microvm(microvm_id, region)

    minutes = int(os.environ.get("TOKEN_EXPIRY_MINUTES", "60"))
    token = create_auth_token(microvm_id, region, minutes)
    print(f"WebSocket : {url}")

    print("Queueing Windows VM ...")
    queued = t6("POST", "/api/user/vm", api_key, {
        "os": "windows",
        "url": url,
        "token": token,
    })
    print("Queued:")
    print(json.dumps(queued, indent=2))

    uid = queued.get("uid") if isinstance(queued, dict) else ""
    if not uid:
        raise SystemExit("Queue response did not include a uid")
    set_uid(microvm_id, uid)

    access = {
        "url": url,
        "token": token,
        "username": user,
        "password": password,
    }
    if key:
        access["key"] = key

    print("Publishing MicroVM desktop ...")
    published = t6("POST", f"/api/user/access/{quote(uid, safe='')}", api_key, access)
    login = published if isinstance(published, dict) else {}
    query = urlencode({
        "username": login.get("username", ""),
        "password": login.get("password", ""),
    })
    print(f"Desktop: https://t6.staticlightlabs.com/microvm?{query}")
    print(json.dumps(published, indent=2))
    print("Stop these MicroVMs with python scripts/stop.py")


if __name__ == "__main__":
    main()
