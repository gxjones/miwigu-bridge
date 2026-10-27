This web lambda enqueues microvm startup jobs. A worker on any machine polls S3, claims a job, and calls `run_microvm` itself. S3 is the only shared state.

One prefix per provider. Queued keys sort oldest-first. Finished jobs are partitioned by date so listings stay small.

```
{provider}/
    queued/{created_ms}-{job_id}.json
    running/{job_id}.json
    locks/{job_id}
    complete/{yyyy}/{mm}/{dd}/{job_id}.json
    failed/{yyyy}/{mm}/{dd}/{job_id}.json
```

## Enqueue

The lambda PUTs `queued/{created_ms}-{job_id}.json` with `If-None-Match: *`. A retry of the same `job_id` does not create a second job.

The object holds the run payload and two presigned PUT URLs, one for the `complete/` key and one for the `failed/` key. Each URL is signed with `If-None-Match: *`, so a second upload of that key fails. The date partition is fixed when the URL is minted, and the URL lasts long enough for startup. Until then the URL is a bearer token for that one object.

## Claim

A worker lists `queued/` and reads the oldest object. It needs list and get on that prefix.

It claims the job by creating `locks/{job_id}` with `If-None-Match: *`. The body is a lease (`owner`, `leased_until`). When the lock already exists and the lease has passed, the worker replaces it with `If-Match` on the current etag. A live lease means another worker owns the job.

The claim then writes `running/{job_id}.json` with the owner and, once `run_microvm` returns it, the microvm id, and deletes the queued key.

## Result

The worker uploads the result JSON to the complete URL or the failed URL, then deletes the lock. Those two keys are the only writes that use the presigned URLs. List, get, the lock, `running/`, and the queued delete use the worker's own credentials.
