A Go HTTP service for the MicroVM queue. It runs as a normal process locally and as a Lambda Function URL in AWS. DynamoDB is the only store. There is no `state.json` on the running service.

`GET /healthz` is unauthenticated. Everything else keeps the same routes as the original server. See [USAGE.md](USAGE.md) for the queue API.

## Table

One DynamoDB table, partition key `pk`, sort key `sk`. No secondary indexes.

| Item | pk | sk | Present while |
|---|---|---|---|
| API key | `key#<sha256>` | `A` | the key exists |
| Key id | `keyid#<id>` | `A` | the key exists |
| Request | `req#<uid>` | `A` | the request exists |
| Desktop | `access#<uid>` | `A` | access is published |
| Viewer login | `login#<username>` | `A` | access is published |
| Queued, one OS | `queue#<os>` | `<createdAt>#<uid>` | `status` is `queued` |
| Queued, any OS | `queue` | `<createdAt>#<os>#<uid>` | `status` is `queued` |
| Launched MicroVM | `microvm#<id>` | `A` | Launch Student has started it |

A claim is one query of the oldest queue row, then a transaction that deletes both queue rows and sets the request to `pending`. Two workers can claim at once; one transaction wins. A deleted request is `status = deleted` on `req#<uid>`. There is no separate deleted list.

Worker result fields are stored as JSON on the request so numbers and nested objects come back the way the worker sent them.

The table is provisioned at 5 reads per second and 5 writes per second, which stays inside the DynamoDB free tier when the account is not already using that allowance. Admin list pages scan. Worker polls query one partition. The template's `BillingMode` parameter switches the table to on-demand if those 5 reads or writes start throttling.

## Local

```bash
docker compose up --build
```

Compose listens on `127.0.0.1:8080` and starts DynamoDB Local. The process creates the table when `DYNAMODB_ENDPOINT` is set and the table is missing.

`ADMIN_PASSWORD` is required. `ADMIN_USER` defaults to `admin`. `DYNAMODB_TABLE` is the table name. `AWS_REGION` defaults to `us-east-1` outside Lambda.

Import an existing `state.json` once. The `deleted` array in that file is ignored. A request whose `status` is `deleted` is what the worker delete check reads.

```bash
DYNAMODB_TABLE=helix DYNAMODB_ENDPOINT=http://127.0.0.1:8000 \
  go run . import /path/to/state.json
```

Against the deployed table, omit `DYNAMODB_ENDPOINT` and use credentials that can write the table.

Tests talk to DynamoDB Local:

```bash
docker run -d --name helix-dynamodb -p 8000:8000 amazon/dynamodb-local:latest \
  -jar DynamoDBLocal.jar -sharedDb -inMemory
make test
```

## Deploy

The function is `provided.al2023`, arm64, 1769 MB (one vCPU), and a Function URL. The [Lambda Web Adapter](https://github.com/aws/aws-lambda-web-adapter) layer (`LambdaAdapterLayerArm64:30`) forwards each invocation to the Go server on port 8080. There is no API Gateway and no VPC. TLS terminates at the Function URL. The app treats that hop as HTTPS, so the session cookie is `Secure`.

From this directory:

```bash
make up
make down
```

`make up` builds the linux/arm64 zip, uploads it, and deploys `template.yaml` as stack `helix-orchestrator`. `ADMIN_PASSWORD` comes from the environment or `.env`. The region is `AWS_REGION`, then `AWS_DEFAULT_REGION`, then `us-east-1`. The code bucket is created in that region when `CODE_BUCKET` is unset.

`make down` deletes that stack and waits until it is gone. The template keeps the table, the artifact bucket, and a MicroVM image the stack built. The code bucket `make up` creates sits outside the stack, so it stays too. `CODE_BUCKET=my-bucket` uses a bucket you already have.

`make up PARAMETERS='BuildImage=true'` turns on the guest image after `app.zip` is in the artifact bucket. `STACK` and `REGION` override the stack name and region. `make zip` is the binary step by itself.

The Function URL is public. The admin password and the API keys are what keep the queue closed.

## MicroVM image

The same template creates the guest image `scripts/03_create_image.py` builds, and the role that build assumes. The image is aarch64, based on `al2023-1`, with the hooks on port 9000. Baseline memory is 2048 MiB. `ALL` is set so the guest can open the tunnel device. The run hook stays enabled, so the WireGuard keys still arrive at start rather than in the zip.

The orchestrator role can call `RunMicrovm`, `GetMicrovm`, `CreateMicrovmAuthToken`, and `TerminateMicrovm` on that image only, and it can pass the managed ingress and internet-egress connectors. The function environment holds the image ARN, those connector ARNs, and the idle times from `config.py` (15 minutes idle, then 5 minutes suspended).

Launch Student calls `RunMicrovm` with that image, both connectors, and the idle policy, then writes `microvm#<id>`. The row keeps the id, endpoint, websocket URL, region, state, image, start time, auth token, and the guacamole key when the run hook was sent. The page asks `GetMicrovm` until the guest is `RUNNING`. `RunMicrovm` returns before the guest is `RUNNING`, and the function does not wait out that boot. Once the guest is `RUNNING`, Launch Student does what `scripts/spawn.py` does next: it queues a Windows VM with the websocket URL and auth token, then publishes the desktop with `GUAC_RDP_USER`, `GUAC_RDP_PASSWORD`, and the guacamole key. The page then opens `/microvm` with the generated username and password. Set both variables in the environment or `.env` before `make up`. `WireGuardPrivateKey` is the guest key sent in the run hook. Without it the MicroVM still starts, the tunnel hook fails, and the desktop is not published.

`BuildImage` defaults to `false`, so a deploy does not wait on a guest build. The stack still creates the artifact bucket. Upload the guest zip, then deploy again with the image enabled:

```bash
python scripts/02_upload_artifact.py   # MICROVM_S3_BUCKET is the stack's artifact bucket
make up PARAMETERS='BuildImage=true'
```

`GuestArtifactBucket` points the image at a bucket that already has `app.zip`. `ImageName=my-first-microvm-image` targets the image the scripts create, without building a second one. The account that deploys with `BuildImage=true` also needs `iam:PassRole` on the build role.
