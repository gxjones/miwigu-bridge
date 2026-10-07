"""
01_setup_iam.py

Creates the IAM role that Lambda assumes during MicroVM image builds.
Idempotent — safe to run multiple times.
"""

import json
import sys
import boto3
from botocore.exceptions import ClientError

sys.path.insert(0, ".")
from config import BUILD_ROLE_NAME, S3_BUCKET, AWS_REGION

TRUST_POLICY = {
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Principal": {"Service": "lambda.amazonaws.com"},
            "Action": ["sts:AssumeRole", "sts:TagSession"],
        }
    ],
}

PERMISSIONS_POLICY = {
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": ["s3:GetObject"],
            "Resource": f"arn:aws:s3:::{S3_BUCKET}/*",
        },
        {
            "Effect": "Allow",
            "Action": [
                "logs:CreateLogGroup",
                "logs:CreateLogStream",
                "logs:PutLogEvents",
            ],
            "Resource": "arn:aws:logs:*:*:*",
        },
    ],
}


def main():
    iam = boto3.client("iam", region_name=AWS_REGION)

    # Create the role (skip if it already exists).
    try:
        response = iam.create_role(
            RoleName=BUILD_ROLE_NAME,
            AssumeRolePolicyDocument=json.dumps(TRUST_POLICY),
            Description="Lambda MicroVM image build role",
        )
        role_arn = response["Role"]["Arn"]
        print(f"Created role: {role_arn}")
    except ClientError as e:
        if e.response["Error"]["Code"] == "EntityAlreadyExists":
            role_arn = iam.get_role(RoleName=BUILD_ROLE_NAME)["Role"]["Arn"]
            print(f"Role already exists: {role_arn}")
        else:
            raise

    # Attach (or update) the inline permissions policy.
    iam.put_role_policy(
        RoleName=BUILD_ROLE_NAME,
        PolicyName="MicrovmBuildPolicy",
        PolicyDocument=json.dumps(PERMISSIONS_POLICY),
    )
    print("Permissions policy attached.")
    print(f"\nBuild role ARN: {role_arn}")
    print("Update config.py if you need to reference this ARN explicitly.")


if __name__ == "__main__":
    main()
