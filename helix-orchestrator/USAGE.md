# Queue API

Base URL is the Function URL, or `http://127.0.0.1:8080` from Compose.

Send the key on every call:

```
Authorization: Bearer $USER_API_KEY
Authorization: Bearer $WORKER_API_KEY
```

Bodies are JSON.

## User

Queue a VM. `os`, `url`, and `token` are required. Returns `{"uid":"…"}`.

```bash
curl -sS "$BASE_URL/api/user/vm" \
  -H "Authorization: Bearer $USER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"os":"windows","url":"wss://example.lambda-microvm.us-east-1.on.aws/","token":"..."}'
```

Read that request. Only the user key that created it can read it.

```bash
curl -sS "$BASE_URL/api/user/vm/$UID" \
  -H "Authorization: Bearer $USER_API_KEY"
```

The response includes `uid`, `os`, `url`, `token`, `status`, and any fields a worker has merged in. `status` is `queued`, `pending`, `success`, `failure`, or `deleted`. After access is published, it also includes that uid's `username` and `password` for `/microvm`.

Publish the MicroVM desktop for that uid. Only the user key that created it can do this. `url`, `token`, `username`, `password`, and `key` are the desktop connection. `key` must be 32 bytes. The response username and password sign in at `/microvm`. Calling this again for the same uid updates the connection and returns the same login.

```bash
curl -sS -X POST "$BASE_URL/api/user/access/$UID" \
  -H "Authorization: Bearer $USER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"url":"wss://example.lambda-microvm.us-east-1.on.aws/","token":"...","username":"Administrator","password":"secret","key":"..."}'
```

Returns `{"username":"vm…","password":"…"}`.

Delete that request. The request status becomes `deleted`.

```bash
curl -sS -X DELETE "$BASE_URL/api/user/vm/$UID" \
  -H "Authorization: Bearer $USER_API_KEY"
```

Returns `{"uid":"…"}`.

## Worker

Claim the oldest queued request for an OS. The status becomes `pending`. The response includes `url` and `token`.

```bash
curl -sS "$BASE_URL/api/worker/vm?os=windows" \
  -H "Authorization: Bearer $WORKER_API_KEY"
```

Omit `os` to claim the oldest request of any OS.

```bash
curl -sS "$BASE_URL/api/worker/vm" \
  -H "Authorization: Bearer $WORKER_API_KEY"
```

Finish a claim. `status` must be `success` or `failure`. Every other field is merged into the object returned by the user status call.

```bash
curl -sS -X PUT "$BASE_URL/api/worker/vm/windows/$UID" \
  -H "Authorization: Bearer $WORKER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"status":"success","host":"10.200.0.9"}'
```

Report which of these running VMs have status `deleted`. The body is a JSON list of uids. The response is the subset that was deleted.

```bash
curl -sS "$BASE_URL/api/worker/deleted" \
  -H "Authorization: Bearer $WORKER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '["a","b"]'
```

A response of `["a"]` means only `a` is deleted.
