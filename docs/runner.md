# Runners

> Status: decided by the [epic 6 review](implementation/06-jobs-and-runner.md) (#36), not implemented yet, but for the queue: jobs, runs, leases, retry, cancellation and coalescing exist in `internal/core/job` on SQLite since #37, and nothing creates a job in production yet; runners, their tokens and the authentication of `/api/runner/` exist since #38, which answers an authenticated request with `404` until #39 adds the routes below and the clone credential, #40 `raun runner`, #41 the screens, #42 the end-to-end test; each updates this page as it lands. `raun runner` still answers that it is not implemented.

A runner is a `raun runner` process that pulls jobs from a Raun server over HTTP, fetches the commit of each job with a short-lived credential, runs it, and reports back. It runs where its owner wants, behind a NAT included: the server never connects to it. It holds what the server never holds, such as LLM credentials (from epic 9), and receives nothing but what its job needs. Why each rule is what it is: [the record](implementation/06-jobs-and-runner.md).

## Create a runner

An administrator of the instance (`RAUN_ADMINS`) creates a runner on the Runners screen of the instance, with:

- **a name**, shown on the runs it takes;
- **a scope**: the instance, whose jobs of every workspace it takes, or one workspace, whose jobs alone it takes;
- **capabilities**: the kinds of job it may take. `noop` exists in epic 6; `check` comes with epic 7, `agents` with epics 9 and 10. The runner cannot declare or widen them itself.

The screen shows the token of the runner once. Raun keeps only its SHA-256: a lost token is regenerated, which refuses the old one at once; a runner still using it stops, and the jobs it was running are lost and go back to the queue. Revoking the runner refuses every later request and sends the jobs it was running back to the queue.

## Run a runner

```sh
RAUN_RUNNER_SERVER=https://raun.example.com \
RAUN_RUNNER_TOKEN_FILE=/etc/raun/runner-token \
raun runner
```

| Variable | Default | Rule |
| --- | --- | --- |
| `RAUN_RUNNER_SERVER` | none, required | The public address of the server; `https`, or `http` on a loopback address only |
| `RAUN_RUNNER_TOKEN` | none, required | The token the Runners screen showed; or `RAUN_RUNNER_TOKEN_FILE`, never both |
| `RAUN_RUNNER_CAPACITY` | `1` | Jobs at a time, 1 to 64 |
| `RAUN_RUNNER_WORKDIR` | `raun-runner` in the temporary directory of the system | Where the directory of each run is created and removed; created if missing |

The server ignores every `RAUN_RUNNER_*` variable, so one environment can hold both. The runner needs Git 2.32 or later and stops at start otherwise.

On `SIGINT` or `SIGTERM` the runner stops claiming and finishes its current jobs, each within its deadline; a second signal stops it at once, and the leases of its jobs expire.

## A job

1. The runner claims a job; the answer gives the run, its lease token, its deadline, the job and a clone credential.
2. It creates an empty directory for the run, fetches the one commit of the job with the credential, and forgets the credential.
3. It runs the handler of the kind, and sends a heartbeat every 15 seconds and the log as it comes.
4. It sends the result, then removes the directory, whatever the outcome.

A heartbeat answered `409`, or that says the job is cancelled, stops the job at once: its result would be refused. A job still running at its deadline is stopped and fails.

The lease lasts 60 seconds. A run whose lease expires is lost, and its job goes back to the queue, three runs at most; a failure the runner reports is final. A job no runner takes fails after 24 hours in the queue.

## Protocol

Version 1. Every route is under `/api/runner/v1/`, takes and gives JSON, and needs:

| Header | Value |
| --- | --- |
| `Authorization` | `Bearer <token of the runner>` |
| `Raun-Runner-Protocol` | `1` |
| `User-Agent` | `raun-runner/<release>` |
| `Raun-Lease` | The lease token of the run, on the routes of a run |

Bodies are decoded strictly: an unknown field or a value out of range is refused with `400`, a body past the bound of its route with `413`. An error answers `{"error": "<what went wrong>"}`, which holds no token.

| Route | Body | Bound | Answers |
| --- | --- | --- | --- |
| `POST /api/runner/v1/claim` | `{}` | 1 KiB | `200` a job, `204` none after up to 30 seconds |
| `POST /api/runner/v1/runs/{run}/heartbeat` | `{}` | 1 KiB | `200` the new end of the lease and whether the job is cancelled; `409` the run is no longer current |
| `POST /api/runner/v1/runs/{run}/log` | `{"seq": n, "text": "..."}` | 512 KiB, and 64 KiB of decoded text | `204`; `409` with the expected `seq`, or for a run no longer current |
| `POST /api/runner/v1/runs/{run}/result` | `{"outcome": "succeeded" \| "failed", "summary": "...", "data": {...}}` | That of the kind (`noop`: 4 KiB) | `204`; `409` for another result, or a run no longer current; `422` a result its kind refuses, which fails the job |

Any route answers `401` to a token that is unknown or revoked, and `400` to a protocol the server does not speak, whether the header or the path names it.

### A normal run

```http
POST /api/runner/v1/claim
Authorization: Bearer [token]
Raun-Runner-Protocol: 1

{}
```

```json
{
  "run": "3e9b0c6a1f2d4e58",
  "lease": {"token": "[lease token]", "expires": "2026-11-14T15:01:00Z"},
  "deadline": "2026-11-14T15:05:00Z",
  "job": {
    "kind": "noop",
    "repository": {"forge": "github", "id": "2001", "full_name": "acme/api"},
    "commit": "9fceb02d0ae598e95dc970b74767f19372d61af8",
    "ref": "refs/heads/main",
    "params": {}
  },
  "clone": {
    "url": "https://github.com/acme/api.git",
    "username": "x-access-token",
    "password": "[clone credential]",
    "expires": "2026-11-14T16:00:00Z"
  }
}
```

The runner then sends heartbeats (`{"expires": "...", "cancel": false}`), log chunks numbered from 0, and its result:

```http
POST /api/runner/v1/runs/3e9b0c6a1f2d4e58/result
Raun-Lease: [lease token]

{"outcome": "succeeded", "summary": "nothing to do", "data": {}}
```

`204`. Sent again after a lost answer, the same result gets `204` again; a different one gets `409`.

### A runner of another protocol

```http
POST /api/runner/v1/claim
Raun-Runner-Protocol: 2
```

`400` with `{"error": "protocol 2 is not spoken here; this server speaks 1"}`. The runner logs it and exits with a non-zero status. A runner and a server of different releases work together while they share a protocol, and a server keeps speaking the previous protocol for at least one release after a new one.

### An expired lease

A runner that could not reach the server for a minute sends a heartbeat: `409` with `{"error": "the run is no longer current"}`. The run was lost and its job may already run elsewhere; the runner stops the job, sends nothing more for it, and removes its directory.

### A revoked runner

Any request: `401`. The runs it held were lost when it was revoked; the runner stops its jobs and exits with a non-zero status.

### A failed fetch

The commit cannot be fetched after three tries (the credential refused, the commit gone, the forge unreachable). The runner reports `{"outcome": "failed", "summary": "the commit could not be fetched"}`, which fails the job without retry. When the server cannot mint the credential, the claim does not give the job and goes on waiting for another; the job may be claimed again 30 seconds later, and no run is counted.

## The clone credential

On GitHub, an installation token of the App for the repository of the job alone, with Contents read, which lasts one hour at most. The server mints it for the run at the claim, holds it in memory only, and revokes it when the run ends. The runner passes it to the one `git fetch --depth 1` of the commit as an HTTP header, through the environment of that Git process alone, never on its command line, in a file or in the configuration of the checkout, and forgets it before the handler starts. It reads the whole repository until then: a runner is trusted with the repositories of the jobs it takes, no more. The head commit of a pull request from a fork is fetched from the base repository.

## Logs

A log is lines of text: the steps of the runner and what the handler of the job writes. It never holds a credential, a URL holding one, the environment, a declared secret or the content of the repository; the runner redacts the clone credential and the secrets it knows. A run keeps 1 MiB of log, ending with `(truncated)` beyond. The page of a run shows it as escaped text to whoever the forge lets read the repository, polled every 2 seconds while the run lasts, and keeps it 30 days.
