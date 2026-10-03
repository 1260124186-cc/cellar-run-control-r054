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
`./data`. The process holds an exclusive lock (`.cellar-control.lock`) on the
data directory while running, so the offline backup and restore commands
refuse to run against a directory that a live service owns.

Environment variables:

- `CELLAR_CONTROL_ADDR`: HTTP listen address.
- `CELLAR_CONTROL_DATA_DIR`: directory containing `state.json`.

Example:

```bash
CELLAR_CONTROL_ADDR=127.0.0.1:9099 \
CELLAR_CONTROL_DATA_DIR=/tmp/cellar-control \
go run ./cmd/fermentctl serve
```

## Backup And Restore (Machine Migration)

The backup and restore commands operate directly on the snapshot directory;
they do not need the HTTP service and must not run while it is using the same
data directory. Use them to move the cellar from one machine to another.

### Back up

```bash
fermentctl backup --data-dir ./data --output-dir ./backups
```

Before writing anything, the command lists every formula, vessel, run,
cleaning fact, and gravity observation that will be included, with totals.
It then writes a self-describing backup envelope containing:

- the complete canonical snapshot as the checksummed `payload`;
- a `manifest` with format version, source machine, revision, row counts,
  and timestamps;
- `payload_sha256` over the canonical payload bytes;
- a SHA-256 signature over the manifest;
- `envelope_sha256` over the full envelope.

The default file name is `cellar-backup-r<revision>-<payload-sha256-prefix>.cellar-backup.json`.
Backups are deterministic in cellar state: running the same backup again
against an unchanged cellar produces identical bytes and reports
`Backup unchanged` after re-verifying the existing file. Use `--output` for an
explicit path, `--note` for an operator note, `--force` to replace an existing
different file, and `--dry-run` to print only the inventory.

### Verify independently

```bash
fermentctl verify-backup --file ./backups/cellar-backup-r12-....cellar-backup.json
```

This recomputes both checksums, authenticates the manifest, validates payload
indexes and referential/lifecycle integrity, and reconciles row counts and
revision. Any single changed byte fails verification before a restore.

### Restore

Run this on the new machine during the downtime window:

```bash
fermentctl restore --data-dir ./data --file ./backups/<backup>.json [--yes]
```

Restore never switches state silently. It first:

1. verifies the backup (checksums, signature, structure, counts);
2. compares the backup against the current target and prints the overwrite
   scope per row — `add`, `overwrite`, or `delete` — plus observations net
   change;
3. blocks with a clear conflict report when the same unique key (formula
   name, vessel code, or run code) is owned by a different id on the target;
4. asks for confirmation (`yes`) unless `--yes` is supplied; non-interactive
   runs without `--yes` are refused.

`--dry-run` performs steps 1–3 without changing anything. When the target
already matches the backup exactly, restore reports that and makes no change.

The actual switch is crash-safe. The new snapshot is fully staged and fsynced
first; the previous `state.json` is copied durably to
`state.pre-restore.json`; only then is the new file renamed into place. An
interruption therefore leaves either the old or the new complete snapshot,
never a partial one. Copy `state.pre-restore.json` back over `state.json` to
roll back.

## Directory Structure

```text
cmd/fermentctl                 service, workflow checks, backup/restore CLI
internal/backup                checksummed backup envelopes, plan, restore
internal/clock                 time boundary
internal/config                environment configuration
internal/domain                entities, validation, transitions, metrics
internal/httpapi               HTTP routes, JSON decoding, errors
internal/service               use-case orchestration
internal/storage               revisioned snapshot persistence and locking
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
```

Each check creates a temporary snapshot directory, starts the real HTTP
handler on a loopback listener, issues JSON requests, verifies success and
failure behavior, and removes its temporary data.

## Persistence And Recovery

The service stores a complete revisioned snapshot at
`<data-directory>/state.json`. A write creates a temporary file in the same
directory, commits the encoded bytes durably, closes the handle, and renames
the result over the previous snapshot. The in-memory snapshot is replaced only
after that operation succeeds.

On startup, an existing snapshot is decoded and normalized. Unknown or
conflicting map indexes are not silently invented; invalid JSON or a negative
revision stops the process before HTTP serving begins.

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
