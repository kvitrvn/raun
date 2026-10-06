# Knowledge file format (v1)

Each knowledge item is one YAML file:

```
.raun/knowledge/<type>/<id>.yaml
```

Raun writes these files in canonical form: fixed field order, two-space indentation, stable ordering. Loading a file and saving it again gives identical bytes, so Git diffs show only real changes. The files are meant to be changed through `raun` commands. Hand edits are possible, but comments are not kept, and `raun check` must still pass afterwards.

## Example

```yaml
version: 1
id: requirement-8fz2mc
type: requirement
title: Issued invoices are immutable
status: contested
requirement:
  statement: An invoice cannot be edited once issued.
  kind: business-rule
  rationale: Legal immutability of issued invoices.
  personas:
    - persona-k3x9q2
support:
  - run: run-20261007-a1
    agent: alpha
    interpretation: i2
    statement: Issued invoices are read-only.
    nature: supported
    observations:
      - statement: Update rejects invoices with status issued.
        evidence:
          - e1
evidence:
  - id: e1
    source: code
    path: billing/invoice.go
    commit: 3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b
    start_line: 40
    end_line: 42
    excerpt: |-
      if inv.Status == StatusIssued {
      	return ErrInvoiceLocked
      }
    sha256: 47267ccb5f30c52c8f86fb18c3a9c19de8a539256970f2a16815483b1e3008c1
    state: verified
  - id: e2
    source: doc
    path: docs/billing.md
    # ...
open_points:
  - id: p1
    kind: disagreement
    summary: Code forbids edits, docs allow a 24h correction window.
    positions:
      - statement: Issued invoices are never editable.
        run: run-20261007-a1
        agents: [alpha]
        evidence: [e1]
      - statement: Admins can edit within 24 hours.
        run: run-20261007-a1
        agents: [beta]
        evidence: [e2]
history:
  - at: 2026-10-07T09:30:00Z
    actor: run
    by: run-20261007-a1
    to: proposed
  - at: 2026-10-07T09:30:00Z
    actor: run
    by: run-20261007-a1
    from: proposed
    to: contested
```

Full examples live in `internal/knowledge/testdata/`.

## Fields

| Field | Content |
|---|---|
| `version` | Format version, `1`. |
| `id` | `<type>-<6 chars>`, random from `23456789abcdefghjkmnpqrstuvwxyz`. Never changes. |
| `type` | `persona` or `requirement`. |
| `title` | Short human label. |
| `status` | See [Statuses](#statuses). Always equals the `to` of the last `history` event. |
| `persona` | Only for type `persona`: `description` (required), `goals[]`, `capabilities[]`. |
| `requirement` | Only for type `requirement`: `statement` (required), `kind` (`business-rule`, `functional`, `constraint`), `rationale`, `personas[]` (persona IDs, which must exist). |
| `support[]` | Agent interpretations backing the item, copied from agent reports: `run`, `agent`, `interpretation` (ID in the report), `statement`, `nature`, `observations[]`. |
| `support[].nature` | `supported`: needs at least one observation. `hypothesis`: an inference the agent declared as such; observations optional. |
| `support[].observations[]` | `statement` + `evidence[]` (at least one evidence ID from this file). |
| `evidence[]` | `id` (`e1`, `e2`, …), `source` (`code`, `doc`, `human-context`), `path` (clean, relative to the repository root), `commit` (full hash), `start_line`, `end_line` (1-based, inclusive), `excerpt`, `sha256` (hex SHA-256 of `excerpt`), `state`. |
| `evidence[].state` | Set by Raun, never by an agent: `verified`, `relocated`, `invalid`, `stale`. |
| `open_points[]` | `id` (`p1`, …), `kind` (`disagreement`, `uncertainty`, `question`), `summary`, `positions[]`, `resolution`. |
| `open_points[].positions[]` | One side of a disagreement, kept as written: `statement`, `run`, `agents[]`, `evidence[]`. A disagreement has at least two positions. |
| `open_points[].resolution` | Set by a human: `at`, `by`, `position` (0-based index of the retained position, optional), `note`. |
| `history[]` | Every status change: `at` (UTC), `actor` (`human` or `run`), `by` (person name or run ID), `from` (absent on creation), `to`, `reason` (required for humans). |

## Statuses

| From | To | Who |
|---|---|---|
| `proposed` | `contested` | run |
| `proposed` | `validated`, `rejected` | human |
| `contested` | `proposed`, `validated` | human, once every disagreement is resolved |
| `contested` | `rejected` | human |
| `validated` | `needs-review` | run |
| `validated` | `rejected` | human |
| `needs-review` | `validated`, `rejected` | human |
| `rejected` | `proposed` | human |

A run can never validate or reject. An item cannot be `validated` while one of its disagreements is unresolved.
