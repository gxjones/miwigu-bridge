"""
02_upload_artifact.py

Zips the WireGuard host guest image from miwigu/wg_host into app.zip,
then uploads it to S3.

The archive is the Docker build context for that Dockerfile: the listener
and the prebuilt aarch64 libraries. Run miwigu/wg_host/build-aarch64.sh
first if those libraries are missing.
"""

import os
import sys
import zipfile
import boto3

sys.path.insert(0, ".")
from config import S3_BUCKET, S3_ARTIFACT_KEY, AWS_REGION

WG_HOST_DIR = os.path.join(os.path.dirname(__file__), "..", "miwigu", "wg_host")
ZIP_PATH = os.path.join(os.path.dirname(__file__), "..", "app.zip")

# Paths are relative to miwigu/wg_host so the Dockerfile COPY lines match.
FILES_TO_ZIP = [
    "Dockerfile",
    "wg_host_listener.py",
    "success_server.py",
    "ssh_server.py",
    "start.sh",
    "dist/aarch64-unknown-linux-gnu/libtunn_ffi.so",
    "dist/aarch64-unknown-linux-gnu/liblwip_port.so",
]


def build_zip():
    zip_path = os.path.abspath(ZIP_PATH)
    wg_host_dir = os.path.abspath(WG_HOST_DIR)

    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
        for filename in FILES_TO_ZIP:
            filepath = os.path.join(wg_host_dir, filename)
            if not os.path.exists(filepath):
                raise FileNotFoundError(
                    f"Expected {filepath}. "
                    "Build the guest libraries with miwigu/wg_host/build-aarch64.sh."
                )
            zf.write(filepath, arcname=filename)
            print(f"  Added {filename}")

    size_kb = os.path.getsize(zip_path) / 1024
    print(f"Created {zip_path} ({size_kb:.1f} KB)")
    return zip_path


def upload_to_s3(zip_path: str):
    s3 = boto3.client("s3", region_name=AWS_REGION)
    print(f"Uploading to s3://{S3_BUCKET}/{S3_ARTIFACT_KEY} ...")
    s3.upload_file(zip_path, S3_BUCKET, S3_ARTIFACT_KEY)
    print("Upload complete.")


def main():
    print("Building zip archive...")
    zip_path = build_zip()
    upload_to_s3(zip_path)
    print(f"\nArtifact available at: s3://{S3_BUCKET}/{S3_ARTIFACT_KEY}")


if __name__ == "__main__":
    main()
