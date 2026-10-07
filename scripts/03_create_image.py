"""
03_create_image.py

Creates a Lambda MicroVM image from the artifact uploaded in step 02,
then polls until the build reaches CREATED (success) or CREATE_FAILED.

Options:
  --force   Delete the existing image first and force a fresh build.
            Use this when you have updated app/ and need to rebuild.
"""

import argparse
import sys
import time
import boto3
from botocore.exceptions import ClientError

sys.path.insert(0, ".")
from config import (
    AWS_REGION,
    IMAGE_NAME,
    S3_BUCKET,
    S3_ARTIFACT_KEY,
    BASE_IMAGE_ARN,
    BUILD_ROLE_NAME,
)

POLL_INTERVAL_SEC = 15
TERMINAL_STATES = {"CREATED", "CREATE_FAILED"}

# The guest listens for these on port 9000. run must be ENABLED or
# RunMicrovm rejects runHookPayload.
HOOKS = {
    "port": 9000,
    "microvmImageHooks": {
        "ready": "ENABLED",
        "readyTimeoutInSeconds": 60,
        "validate": "ENABLED",
        "validateTimeoutInSeconds": 30,
    },
    "microvmHooks": {
        "run": "ENABLED",
        "runTimeoutInSeconds": 10,
        "resume": "ENABLED",
        "resumeTimeoutInSeconds": 10,
        "suspend": "ENABLED",
        "suspendTimeoutInSeconds": 10,
        "terminate": "ENABLED",
        "terminateTimeoutInSeconds": 10,
    },
}


def get_build_role_arn(iam_client) -> str:
    return iam_client.get_role(RoleName=BUILD_ROLE_NAME)["Role"]["Arn"]


def get_account_id() -> str:
    return boto3.client("sts").get_caller_identity()["Account"]


def delete_image_if_exists(microvms, image_arn: str) -> bool:
    """Delete the image and wait until it is gone. Returns True if it existed."""
    try:
        microvms.delete_microvm_image(imageIdentifier=image_arn)
        print(f"Deleting existing image {image_arn} ...")
    except ClientError as e:
        code = e.response["Error"]["Code"]
        if code in ("ResourceNotFoundException", "NotFoundException"):
            return False
        raise

    # Poll until the image is gone (DELETE_FAILED is treated as an error)
    while True:
        try:
            status = microvms.get_microvm_image(imageIdentifier=image_arn)
            state = status.get("state", "UNKNOWN")
            print(f"  [{time.strftime('%H:%M:%S')}] delete state={state}")
            if state == "DELETE_FAILED":
                print("Image deletion failed. Check the AWS console.")
                sys.exit(1)
        except ClientError as e:
            code = e.response["Error"]["Code"]
            if code in ("ResourceNotFoundException", "NotFoundException"):
                print("Image deleted.\n")
                return True
            raise
        time.sleep(POLL_INTERVAL_SEC)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--force",
        action="store_true",
        help="Delete the existing image first and force a fresh build.",
    )
    args = parser.parse_args()

    iam = boto3.client("iam", region_name=AWS_REGION)
    microvms = boto3.client("lambda-microvms", region_name=AWS_REGION)

    build_role_arn = get_build_role_arn(iam)
    print(f"Using build role: {build_role_arn}")

    # Construct the ARN up front — get_microvm_image requires it, not just the name.
    account_id = get_account_id()
    image_arn = f"arn:aws:lambda:{AWS_REGION}:{account_id}:microvm-image:{IMAGE_NAME}"

    if args.force:
        delete_image_if_exists(microvms, image_arn)

    try:
        print(f"Creating MicroVM image '{IMAGE_NAME}' ...")
        response = microvms.create_microvm_image(
            name=IMAGE_NAME,
            codeArtifact={"uri": f"s3://{S3_BUCKET}/{S3_ARTIFACT_KEY}"},
            baseImageArn=BASE_IMAGE_ARN,
            buildRoleArn=build_role_arn,
            hooks=HOOKS,
        )
        image_arn = response.get("imageArn", image_arn)
        print(f"Image ARN: {image_arn}")
    except ClientError as e:
        code = e.response["Error"]["Code"]
        msg = e.response["Error"].get("Message", "")
        if code in ("ConflictException", "ResourceConflictException") or (
            code == "ValidationException" and "already exists" in msg
        ):
            print(f"Image already exists. Run with --force to rebuild from scratch.")
            print(f"Polling existing build: {image_arn}")
        else:
            raise

    print("Polling for build completion (this can take several minutes)...\n")

    while True:
        status = microvms.get_microvm_image(imageIdentifier=image_arn)
        state = status.get("state", "UNKNOWN")
        version = status.get("imageVersion", "?")
        print(f"  [{time.strftime('%H:%M:%S')}] state={state}  version={version}")

        if state in TERMINAL_STATES:
            break

        time.sleep(POLL_INTERVAL_SEC)

    if state == "CREATED":
        print(f"\nImage '{IMAGE_NAME}' is ready.")
        print(f"Image ARN: {image_arn}")
    else:
        log_group = f"/aws/lambda/microvms/{IMAGE_NAME}"
        print(f"\nBuild FAILED. Check CloudWatch logs at: {log_group}")
        sys.exit(1)


if __name__ == "__main__":
    main()
