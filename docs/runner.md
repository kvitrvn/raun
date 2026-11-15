# Runners

> Status: decided by the [epic 6 review](implementation/06-jobs-and-runner.md) (#36). Jobs, runs, leases, retry, cancellation and coalescing exist in `internal/core/job` on SQLite since #37, and nothing creates a job in production yet; runners, their tokens and the authentication of `/api/runner/` since #38; the protocol below, the clone credential and the logs since #39: the routes in `internal/adapter/driving/web`, the messages in `internal/adapter/runnerapi`, the long poll and the credentials in `job.Dispatcher`; `raun runner` since #40: its loop in `job.Loop`, the client of the protocol in `internal/adapter/driven/runnerclient`, the fetch of a commit in `internal/adapter/driven/git`. the screens since #41: the Runs list of a repository, the page of a job with its live log, the cancellation by hand, and the Runners screen with the last contact, the release and the current runs ([docs/web.md](web.md#runs)). #42 brings the end-to-end test.

A runner is a `raun runner` process that pulls jobs from a Raun server over HTTP, fetches the commit of each job with a short-lived credential, runs it, and reports back. It runs where its owner wants, behind a NAT included: the server never connects to it. It holds what the server never holds, such as LLM credentials (from epic 9), and receives nothing but what its job needs. Why each rule is what it is: [the record](implementation/06-jobs-and-runner.md).

## Create a runner

An administrator of the instance (`RAUN_ADMINS`) creates a runner on the Runners screen of the instance, with:

- **a name**, shown on the runs it takes;
- **a scope**: the instance, whose jobs of every workspace it takes, or one workspace, whose jobs alone it takes;
- **capabilities**: the kinds of job it may take. `noop` exists in epic 6; `check` comes with epic 7, `agents` with epics 9 and 10. The runner cannot declare or widen them itself.

The screen shows the token of the runner once. Raun keeps only its SHA-256: a lost token is regenerated, which refuses the old one at once; a runner still using it stops, and the jobs it was running are lost and go back to the queue. Revoking the runner refuses every later request and sends the jobs it was running back to the queue.

## Run a runner

`raun runner` is the same binary as the server (`make build` gives `bin/raun`). It needs Git 2.32 or later, with `git` in its `PATH`, and checks it at start. Put the token the Runners screen showed in a file only the user of the runner reads:

```sh
install -m 0600 /dev/null /etc/raun/runner-token   # then write the token into it
RAUN_RUNNER_SERVER=https://raun.example.com \
RAUN_RUNNER_TOKEN_FILE=/etc/raun/runner-token \
raun runner
```

| Variable | Default | Rule |
| --- | --- | --- |
| `RAUN_RUNNER_SERVER` | none, required | The public address of the server; `https`, or `http` on a loopback address only |
| `RAUN_RUNNER_TOKEN` | none, required | The token the Runners screen showed; or `RAUN_RUNNER_TOKEN_FILE`, never both |
| `RAUN_RUNNER_CAPACITY` | `1` | Jobs at a time, 1 to 64 |
| `RAUN_RUNNER_WORKDIR` | `raun-runner-<uid>` in the temporary directory of the system, one per user | Where the directory of each run is created and removed; created if missing, and held by one runner |

The server ignores every `RAUN_RUNNER_*` variable, and the runner every other one, so one environment can hold both; a `RAUN_RUNNER_*` variable the runner does not know stops it at start, named without its value. The runner logs one JSON record per line on standard error, its configuration first, the token as `[redacted]`.

The work directory belongs to one runner. At start, the runner creates it if it is missing, refuses a link or a directory of another user, closes it to other users (`0700`), and takes the lock of its file `.lock`, held while it runs: a second runner on the same directory refuses to start, so give each runner of a user its own. It then removes the directories of runs a killed runner left there, those whose name starts with `run-`, and nothing else; a link is removed, never followed.

On `SIGINT` or `SIGTERM` the runner stops claiming and finishes its current jobs, each within its deadline; a second signal stops it at once, and the leases of its jobs expire. It then exits with `0`. It exits with `1` when its configuration is invalid, when Git is missing or older than 2.32, and when the server refuses it: an unknown or revoked token (`401`), or a protocol the server does not speak (`400`), which stops it at once, its jobs abandoned.

## A job

1. The runner claims a job when a slot is free; the answer gives the run, its lease token, its deadline, the job and a clone credential. A claim that fails for want of a server is tried again after 1 second, then a wait doubled each time, up to 30 seconds.
2. It creates an empty directory for the run, fetches the one commit of the job with the credential, three tries 1 then 2 seconds apart, and forgets the credential: the handler never sees it. Three failed tries fail the job: `the commit could not be fetched`.
3. It runs the handler of the kind, and sends a heartbeat every 15 seconds, from the claim on, the fetch included, and the log every 2 seconds, apart from the heartbeats, which a log that cannot be sent never holds up.
4. It sends the rest of the log, three tries for a chunk, then drops what it could not send, so the result goes either way; it checks the result by its kind, and fails the job with `the handler gave a result its kind refuses` rather than send one the server would refuse; then it sends the result and removes the directory, whatever the outcome. A result that gets no answer is sent again, the same, 1 second later, then a wait doubled each time up to 10 seconds, while the lease and the deadline hold: a failure of the server or the network does not lose it.

A job of a kind the runner has no handler for fails; a handler that fails or panics fails its job with `the handler failed`, and what it said stays in the log of the runner, never in the log of the run. `noop` succeeds with `nothing to do`.

A heartbeat answered `409`, or that says the job is cancelled, stops the job at once, and nothing more is sent for it: its result would be refused. A heartbeat that gets no answer is tried again at the next beat, until the lease the server last gave has passed, which stops the job too. A job still running at its deadline is stopped, and the server fails it.

The lease lasts 60 seconds. A run whose lease expires is lost, and its job goes back to the queue, three runs at most; a failure the runner reports is final. A job no runner takes fails after 24 hours in the queue.

## Protocol

Version 1. Every route is under `/api/runner/v1/`, takes and gives JSON, and needs:

| Header | Value |
| --- | --- |
| `Authorization` | `Bearer <token of the runner>` |
| `Raun-Runner-Protocol` | `1` |
| `User-Agent` | `raun-runner/<release>` |
| `Raun-Lease` | The lease token of the run, on the routes of a run |

Every answer with a body is JSON. Bodies are decoded strictly: anything but one JSON object, an unknown field, a chunk without `seq` or `text`, or a value out of range is refused with `400`, a body past the bound of its route with `413`. A `User-Agent` that is not `raun-runner/<release>`, a release of 1 to 64 letters, digits and `.+-_`, is refused with `400`; the server records the release on the runner. A route of a run without one `Raun-Lease` header is refused with `400`. An error answers `{"error": "<what went wrong>"}`, which holds no token; an unknown route of protocol 1 answers `404`, another method than `POST` `405`.

| Route | Body | Bound | Answers |
| --- | --- | --- | --- |
| `POST /api/runner/v1/claim` | `{}` | 1 KiB | `200` a job, `204` none after up to 30 seconds, or at once when the server stops; `401` at once for a runner revoked while it waits |
| `POST /api/runner/v1/runs/{run}/heartbeat` | `{}` | 1 KiB | `200` `{"expires": "...", "cancel": false}`, the new end of the lease, or `{"cancel": true}` for a cancelled job; `409` the run is no longer current |
| `POST /api/runner/v1/runs/{run}/log` | `{"seq": n, "text": "..."}` | 512 KiB, and 64 KiB of decoded text (`413` past it) | `204`, for a chunk sent again too; `409` `{"error": "...", "expected": n}` for a chunk that skips a number, or a run no longer current or cancelled; `400` a negative `seq` |
| `POST /api/runner/v1/runs/{run}/result` | `{"outcome": "succeeded" \| "failed", "summary": "...", "data": {...}}` | The data bound of the kind plus 8 KiB (`noop`: 12 KiB) | `204`; `409` for another result, or a run no longer current or cancelled; `422` a result its kind refuses, which fails the job |

A run is no longer current when its lease token is not the one of the current run of its job, when the run holds no lease any more, or when it belongs to another runner: each answers the same `409`.

Any route answers `401` to a token that is unknown or revoked, and `400` to a protocol the server does not speak, whether the header or the path names it, or when they name different ones; every path below `/api/runner/` is the runner API's.

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

The runner then sends heartbeats (`{"expires": "...", "cancel": false}`, `{"cancel": true}` once the job is cancelled), log chunks numbered from 0, and its result:

```http
POST /api/runner/v1/runs/3e9b0c6a1f2d4e58/result
Raun-Lease: [lease token]

{"outcome": "succeeded", "summary": "nothing to do", "data": {}}
```

`204`. Sent again after a lost answer, the same result, byte for byte, gets `204` again; a different one gets `409`.

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

On GitHub, an installation token of the App for the repository of the job alone, with Contents read, which lasts one hour at most. The server mints it for the run at the claim, apart from the tokens it keeps for its own reads, holds it in memory only, and revokes it when the run ends: at once for a result, accepted or refused, and for a heartbeat, a log or a result that finds the run cancelled or no longer current; within 15 seconds, at the next pass of the reaper, for a run ended otherwise, by its lease, its deadline, a cancellation, the revocation of its runner or the deactivation of its repository. A revocation that fails is tried again at each pass until the credential expires. A job cancelled while its credential is minted is not given, and the credential is revoked at once. The runner passes it to the one `git fetch --depth 1` of the commit as an HTTP Basic header, through the environment of that Git process alone (`GIT_CONFIG_COUNT`, `http.extraHeader`), never on its command line, in a file or in the configuration of the checkout, and forgets it before the handler starts. The checkout is a bare repository holding that commit, fetched by its hash: the forge must let a reachable commit be fetched by its hash, as GitHub does. Git runs with no user or system configuration, no hook, no template, no credential helper, no redirect and no submodule, the objects received checked, and nothing it writes reaches the log of the run. It reads the whole repository until then: a runner is trusted with the repositories of the jobs it takes, no more. The head commit of a pull request from a fork is fetched from the base repository.

## Logs

A log is lines of text: the steps of the runner and what the handler of the job writes. It never holds a credential, a URL holding one, the environment, a declared secret or the content of the repository; the runner redacts the clone credential and the secrets it knows, in every form a header shows them and across two writes; the start of one that no later write completes, at the cut or at the end of the run, is redacted too. The steps the runner writes are `fetching <commit>`, `fetched` or `the commit could not be fetched, try n of 3`, `running <kind>`, and `<outcome>: <summary>`. It sends the log in chunks of 64 KiB at most, each kept, the same text under the same number, until the server took it. A run keeps 1 MiB of log, cut at a character and ending with a line `(truncated)` beyond; the numbers of later chunks are still taken, their text dropped. Text that is not UTF-8 is refused with `400`. The page of a job shows the log of one of its runs as escaped text to whoever the forge lets read the repository, polled every 2 seconds from the last byte shown while the run lasts; the server keeps it 30 days after the run ended, purged hourly with the events.
