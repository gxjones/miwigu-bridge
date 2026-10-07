import os

# --- Required: set these before running any scripts ---

# The S3 bucket where the app artifact (app.zip) will be uploaded.
S3_BUCKET = os.environ.get("MICROVM_S3_BUCKET", "your-bucket-name")

# AWS region to operate in. Must support lambda-microvms.
AWS_REGION = os.environ.get("AWS_DEFAULT_REGION", "us-east-1")

# --- IAM ---

# Name of the IAM role that Lambda assumes during image builds.
BUILD_ROLE_NAME = "MicrovmBuildRole"

# --- MicroVM Image ---

# Friendly name for the MicroVM image.
IMAGE_NAME = "my-first-microvm-image"

# S3 key for the uploaded artifact.
S3_ARTIFACT_KEY = "app.zip"

# Lambda-published managed base image ARN.
# al2023-1 is the Amazon Linux 2023 base image.
BASE_IMAGE_ARN = f"arn:aws:lambda:{AWS_REGION}:aws:microvm-image:al2023-1"

# --- Network connectors (Lambda-managed defaults) ---

INGRESS_CONNECTOR_ARN = (
    f"arn:aws:lambda:{AWS_REGION}:aws:network-connector:aws-network-connector:ALL_INGRESS"
)
EGRESS_CONNECTOR_ARN = (
    f"arn:aws:lambda:{AWS_REGION}:aws:network-connector:aws-network-connector:INTERNET_EGRESS"
)

# --- Idle policy ---

IDLE_POLICY = {
    "autoResumeEnabled": True,
    # Suspend the MicroVM after 15 minutes of no traffic.
    "maxIdleDurationSeconds": 900,
    # Terminate the MicroVM if it stays suspended longer than 5 minutes.
    "suspendedDurationSeconds": 300,
}

# --- Local state file (written by 04_run_microvm.py, read by 05/06) ---

STATE_FILE = "state.json"
