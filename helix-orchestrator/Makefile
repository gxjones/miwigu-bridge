.PHONY: zip test up down

# linux/arm64 matches the Function's architecture in template.yaml.
zip:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/bootstrap .
	rm -f dist/bootstrap.zip
	cd dist && zip -X bootstrap.zip bootstrap
	rm -f dist/bootstrap

test:
	DYNAMODB_ENDPOINT=$${DYNAMODB_ENDPOINT:-http://127.0.0.1:8000} go test ./...

# make up builds the zip, uploads it, and deploys template.yaml.
# make down deletes that stack.
# ADMIN_PASSWORD comes from the environment or .env.
# REGION defaults to AWS_REGION, then AWS_DEFAULT_REGION, then us-east-1.
# CODE_BUCKET defaults to a bucket this Makefile creates. Set it to keep your own.
# PARAMETERS is extra CloudFormation overrides, for example BuildImage=true.
STACK ?= helix-orchestrator
FUNCTION ?= helix-orchestrator
REGION ?= $(or $(AWS_REGION),$(AWS_DEFAULT_REGION),us-east-1)
PARAMETERS ?=

ifneq (,$(wildcard .env))
include .env
export
endif

up:
	@if [ -z "$${ADMIN_PASSWORD:-}" ]; then \
	  echo "ADMIN_PASSWORD is required. Put it in .env or the environment." >&2; \
	  exit 1; \
	fi
	@$(MAKE) zip
	@set -euo pipefail; \
	region="$(REGION)"; \
	stack="$(STACK)"; \
	account=$$(aws sts get-caller-identity --query Account --output text --region "$$region"); \
	bucket="$(CODE_BUCKET)"; \
	if [ -z "$$bucket" ]; then bucket="helix-orchestrator-code-$$account-$$region"; fi; \
	if ! aws s3api head-bucket --bucket "$$bucket" --region "$$region" >/dev/null 2>&1; then \
	  aws s3 mb "s3://$$bucket" --region "$$region"; \
	  aws s3api put-public-access-block --bucket "$$bucket" --region "$$region" \
	    --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true; \
	fi; \
	digest=$$(openssl dgst -sha256 dist/bootstrap.zip | awk '{print $$NF}'); \
	key="helix-orchestrator/bootstrap-$$digest.zip"; \
	aws s3 cp dist/bootstrap.zip "s3://$$bucket/$$key" --region "$$region"; \
	set -- \
	  "CodeBucket=$$bucket" \
	  "CodeKey=$$key" \
	  "FunctionName=$(FUNCTION)" \
	  "AdminPassword=$$ADMIN_PASSWORD"; \
	if [ -n "$${WG_SERVER_PRIVATE_KEY:-}" ]; then set -- "$$@" "WireGuardPrivateKey=$$WG_SERVER_PRIVATE_KEY"; fi; \
	if [ -n "$${WG_PEER_PUBLIC_KEY:-}" ]; then set -- "$$@" "WireGuardPeerPublicKey=$$WG_PEER_PUBLIC_KEY"; fi; \
	if [ -n "$${GUAC_RDP_USER:-}" ]; then set -- "$$@" "GuacRdpUser=$$GUAC_RDP_USER"; fi; \
	if [ -n "$${GUAC_RDP_PASSWORD:-}" ]; then set -- "$$@" "GuacRdpPassword=$$GUAC_RDP_PASSWORD"; fi; \
	aws cloudformation deploy \
	  --region "$$region" \
	  --template-file template.yaml \
	  --stack-name "$$stack" \
	  --capabilities CAPABILITY_IAM \
	  --no-fail-on-empty-changeset \
	  --parameter-overrides "$$@" $(PARAMETERS); \
	url=$$(aws cloudformation describe-stacks --region "$$region" --stack-name "$$stack" \
	  --query "Stacks[0].Outputs[?OutputKey=='FunctionUrl'].OutputValue" --output text); \
	printf 'function url: %s\n' "$$url"

down:
	aws cloudformation delete-stack --region $(REGION) --stack-name $(STACK)
	aws cloudformation wait stack-delete-complete --region $(REGION) --stack-name $(STACK)
