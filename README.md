# Cellar Run Control

Cellar Run Control is a local Go HTTP service for coordinating a small
production cellar. It keeps formula approval, vessel availability, active
fermentation runs, gravity observations, conditioning, completion metrics, and
vessel cleaning in one deterministic process.

This repository is a healthy initialization baseline. It has no intentional
defects and no automated test suite. Tests are intentionally deferred to a
later engineering task; the production workflow checks in this repository
remain available for immediate build and behavior verification.

## Requirements

- Go 1.23 or later
- A writable local data directory
- No database, network service, or third-party module

## Build And Run

Build every package:

```bash
go build ./...
```

Start the service:

```bash
go run ./cmd/fermentctl serve
```

The default address is `127.0.0.1:8088`. The default snapshot directory is
`./data`.

## Backup, Inspect, And Restore (Machine Migration)

Three standalone commands move a cellar between machines without going through
the HTTP API. They read the same `state.json` file and accept `--data-dir`
(or `-d`), which defaults to `CELLAR_CONTROL_DATA_DIR` or `./data`.

Back up the current cellar. The command first prints the complete inventory it
will contain (formulas, vessels, runs with their gravity observation counts,
and cleaning facts), then writes one self-contained, checksummed JSON archive:

```bash
fermentctl backup --data-dir /var/lib/cellar -o migration.json
```

Re-running the same backup has an explicit, non-destructive result:

- the same path already holding the same bytes or the same payload is reused
  and nothing is rewritten;
- another archive in the same directory already embedding the identical
  snapshot is detected, its path is reported, and no duplicate file is written;
- a different file at the output path is refused unless `--force` (`-f`)
  replaces it atomically.

Verify an archive independently before a maintenance window:

```bash
fermentctl inspect-backup migration.json          # full report and contents
fermentctl inspect-backup --quiet migration.json  # checksum and verdict only
```

Inspection recomputes the SHA-256 over the embedded payload and then validates
the snapshot itself: manifest counts, lookup indexes, entity rules, observation
sequences, recomputed completion metrics, and every run/vessel/formula
cross-reference. A failed checksum or any semantic defect exits non-zero and the
archive must not be restored.

Restore during the stop window. The command verifies the backup, then prints the
full scope before changing anything: what is added and overwritten per entity,
what target-only data is removed, the resulting observation count, and every
unique-key conflict (same name/code already owned by a different target ID) with
its resolution. Nothing changes until `yes` is typed (or `--yes`/`-y` is given):

```bash
fermentctl restore --data-dir /var/lib/cellar migration.json
```

Restore safety properties:

- the running service holds a shared data-directory lock and a restore requires
  an exclusive lock, so restoring while the service is serving fails loudly
  instead of being silently overwritten later by the old in-memory state;
- the previous live snapshot is preserved atomically at a timestamped
  `<data-dir>/state.pre-restore-<timestamp>.json` (override with `--safety-copy`;
  an existing file at a custom path is refused unless `--replace-safety-copy`),
  so repeated restores keep every rollback point;
- the replacement is written to a temporary file, fsynced, and renamed into
  place, so an interruption can never expose a half-written `state.json`;
- the installed bytes are re-read, normalized, and hashed against the verified
  backup; on mismatch the previous snapshot is rolled back automatically;
- if the target changed between printing the scope and confirming, the restore
  aborts and the live data stays untouched;
- restoring the same backup twice is harmless: the second run reports every
  record as already identical and keeps a fresh safety copy.

Environment variables:

- `CELLAR_CONTROL_ADDR`: HTTP listen address.
- `CELLAR_CONTROL_DATA_DIR`: directory containing `state.json`.

Example:

```bash
CELLAR_CONTROL_ADDR=127.0.0.1:9099 \
CELLAR_CONTROL_DATA_DIR=/tmp/cellar-control \
go run ./cmd/fermentctl serve
```

## Directory Structure

```text
cmd/fermentctl                 service, offline backup commands, workflow checks
internal/backup                checksummed archive format, verification, restore planning
internal/clock                 time boundary
internal/config                environment configuration
internal/domain                entities, validation, transitions, metrics
internal/httpapi               HTTP routes, JSON decoding, errors
internal/service               use-case orchestration
internal/storage               revisioned snapshot persistence, locking, atomic replace
internal/workflowcheck         real HTTP production checks
```

## Public API

Health:

- `GET /healthz`

Formula workflow:

- `POST /v1/formulas`
- `GET /v1/formulas`
- `GET /v1/formulas/{id}`
- `PUT /v1/formulas/{id}/steps`
- `POST /v1/formulas/{id}/approve`
- `POST /v1/formulas/{id}/retire`

Vessel workflow:

- `POST /v1/vessels`
- `GET /v1/vessels`
- `GET /v1/vessels/{id}`
- `POST /v1/vessels/{id}/finish-cleaning`
- `POST /v1/vessels/{id}/retire`

Run workflow:

- `POST /v1/runs`
- `GET /v1/runs`
- `GET /v1/runs/{id}`
- `POST /v1/runs/{id}/reserve`
- `POST /v1/runs/{id}/start`
- `POST /v1/runs/{id}/observations`
- `POST /v1/runs/{id}/conditioning`
- `POST /v1/runs/{id}/complete`
- `POST /v1/runs/{id}/abort`

Writes accept an optional `X-Expected-Version` header. Errors use a stable
shape:

```json
{
  "error": {
    "code": "revision_conflict",
    "detail": "the aggregate version does not match X-Expected-Version",
    "field": ""
  }
}
```

## Workflow Checks

Run each production workflow check separately:

```bash
go run ./cmd/fermentctl check formula-approval
go run ./cmd/fermentctl check vessel-run-lifecycle
go run ./cmd/fermentctl check observations-completion
go run ./cmd/fermentctl check backup-restore
```

The first three checks create a temporary snapshot directory, start the real
HTTP handler on a loopback listener, issue JSON requests, verify success and
failure behavior, and remove their temporary data. The `backup-restore` check
drives the offline package directly against real snapshot files: preview and
manifest counts, idempotent backup creation, checksum tampering, semantically
broken payload rejection, restore onto empty and drifted targets, unique-key
collision scope and resolution, stale-plan rejection, safety copies, and
byte-exact reopening.

## Persistence And Recovery

The service stores a complete revisioned snapshot at
`<data-directory>/state.json`. A write creates a temporary file in the same
directory, commits the encoded bytes durably, closes the handle, and renames
the result over the previous snapshot. The in-memory snapshot is replaced only
after that operation succeeds.

On startup, an existing snapshot is decoded and normalized. Unknown or
conflicting map indexes are not silently invented; invalid JSON or a negative
revision stops the process before HTTP serving begins.

A `cellar.lock` advisory file in the data directory coordinates the service
with the offline commands: the service and `backup` hold a shared lock and
`restore` requires an exclusive lock. Restoring while the service is running is
rejected, and a timestamped `state.pre-restore-*.json` preserves the live
snapshot for rollback.

## Repair-Ready Boundaries

The baseline intentionally keeps several independent behavior surfaces visible
for later engineering evaluation:

- formula name normalization and duplicate detection;
- target gravity and process-step range validation;
- aggregate version conflict checks;
- atomic vessel reservation and release;
- run state transition guards;
- observation sequence and time ordering;
- gravity movement and range checks;
- metric calculation and rounding;
- durable snapshot replacement and startup normalization.

These are production mechanisms, not injected defects. A later task can add
automated tests or request a focused repair without changing the public API.

## Test Boundary

This stage does not create unit tests, integration tests, test fixtures, or an
E2E suite. The later test task owns systematic coverage of validation limits,
state transitions, concurrency, persistence failures, and numeric edge cases.

## Scope

The service does not control physical equipment, authenticate remote users,
send notifications, expose a browser interface, or replicate data across
processes. It is intended for a trusted local network or an internal adapter.
