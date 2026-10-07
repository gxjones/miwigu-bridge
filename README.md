# AWS Lambda MicroVM Sandbox

A place for Grant Jones to experiment with AWS Lambda MicroVMs.

Documentation:
- [Lambda MicroVMs overview](https://docs.aws.amazon.com/lambda/latest/dg/lambda-microvms-guide.html)
- [How it works / core concepts](https://docs.aws.amazon.com/lambda/latest/dg/microvms-how-it-works.html)
- [Getting started tutorial](https://docs.aws.amazon.com/lambda/latest/dg/microvms-getting-started.html)

---

## What's in this repo

```
aws_lambda_microvm_sandbox/
├── config.py                   # Shared constants — edit before running
├── requirements.txt
├── app/
│   ├── app.js                  # Node.js HTTP + WebSocket terminal server (port 8080)
│   ├── package.json            # ws + node-pty dependencies
│   └── Dockerfile
└── scripts/
    ├── 01_setup_iam.py         # Create IAM build role
    ├── 02_upload_artifact.py   # Zip & upload app/ to S3
    ├── 03_create_image.py      # Build MicroVM image, poll until ready
    ├── 04_run_microvm.py       # Launch a MicroVM, poll until RUNNING
    ├── 05_connect.py           # Get auth token, hit the endpoint
    ├── 06_terminate.py         # Terminate MicroVM and clean up
    └── 07_terminal.py          # Open a web terminal connected to the MicroVM
```

---

## Prerequisites

1. An existing **S3 bucket** in your target region.
2. AWS credentials configured locally (`~/.aws/credentials` or environment variables) with permissions to create IAM roles and access S3.
3. The `lambda-microvms` service available in your chosen region.

---

## Setup

```bash
pip install -r requirements.txt
```

Open `config.py` and set at minimum:

| Variable | Description |
|---|---|
| `S3_BUCKET` | Your S3 bucket name (or set `MICROVM_S3_BUCKET` env var) |
| `AWS_REGION` | Target region (or set `AWS_DEFAULT_REGION` env var) |

---

## Running the walkthrough

Run the scripts in order from the repo root:

```bash
# 1. Create the IAM build role (idempotent — safe to run again)
python scripts/01_setup_iam.py

# 2. Package app/ and upload to S3
python scripts/02_upload_artifact.py

# 3. Build the MicroVM image (takes a few minutes)
python scripts/03_create_image.py

# 4. Launch a MicroVM and wait for it to reach RUNNING
python scripts/04_run_microvm.py

# 5. Create an auth token and send a test request
python scripts/05_connect.py

# 6. Terminate the MicroVM and release all resources
python scripts/06_terminate.py
```

`state.json` is created by script 04 and read by scripts 05, 06, and 07. It is deleted on termination.

---

## Web terminal

Script 07 opens an interactive browser-based terminal connected to a shell running inside the MicroVM:

```bash
python scripts/07_terminal.py
```

This starts a local proxy server at `http://localhost:8765` and opens your browser automatically. The proxy:

1. Fetches a short-lived AWS auth token via boto3
2. Serves an [xterm.js](https://xtermjs.org/) terminal page in your browser
3. Accepts WebSocket connections from the browser and forwards them to the MicroVM, injecting the `X-aws-proxy-auth` header (browsers cannot set this header themselves on WebSocket upgrade requests)

The MicroVM app (`app/app.js`) runs a PTY-backed `bash` session on the WebSocket endpoint `wss://{endpoint}/ws`. Terminal resize events are propagated automatically.

> **Note:** If you previously built the MicroVM image before adding the terminal, you must rebuild it to pick up the new app:
> ```bash
> python scripts/02_upload_artifact.py   # re-upload with package.json included
> python scripts/03_create_image.py      # rebuild the image
> python scripts/04_run_microvm.py       # launch a new MicroVM
> ```

---

## MicroVM lifecycle

```
PENDING → RUNNING → SUSPENDING → SUSPENDED
                ↑_________________________↑  (auto-resume on traffic)
RUNNING / SUSPENDED → TERMINATING → TERMINATED
```

The idle policy in `config.py` suspends the MicroVM after 15 minutes of
inactivity and auto-resumes it when traffic arrives.
