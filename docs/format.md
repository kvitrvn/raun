# Knowledge format

> Status: decided by the [epic 2 review](implementation/02-knowledge-core.md) (#9). The item model, its validation, its identifiers and its transitions are implemented in `internal/core/knowledge` (#10). Evidence verification (#11, #13) and storage (#14) are not implemented yet: for them, this page is the specification.

This page defines a knowledge item: its fields, its statuses and who may change them, its evidence and how Raun verifies it, and the versions of Raun's formats. The model is the one of raun-old (`docs/format.md` at `330e634`), stored in a database instead of YAML files.

## Knowledge item

An item belongs to one repository of one workspace. Its fields:

| Field | Content |
| --- | --- |
| `id` | `<type>-<6 characters>`, see [Identifiers](#identifiers). Never changes. |
| `type` | `persona` or `requirement`. |
| `title` | Short human label. Required. |
| `status` | See [Statuses](#statuses). Always equals the `to` of the last history event. |
| `revision` | See [Revisions](#revisions). |
| `persona` | Only for type `persona`: `description` (required), `goals[]`, `capabilities[]`. |
| `requirement` | Only for type `requirement`: `statement` (required), `kind` (optional; a value outside the kinds below is refused), `rationale`, `personas[]` (identifiers of persona items of the same repository, which must exist; the store checks it inside the write transaction). |
| `support[]` | The agent interpretations backing the item. |
| `evidence[]` | The citations the support rests on. |
| `open_points[]` | Disagreements, uncertainties and questions. |
| `history[]` | Every status change. See [History](#history). |
| `created_at`, `updated_at` | UTC. |

An item is never deleted on its own: a human rejects it. Items disappear only with their repository (epic 13), so a persona reference never points at nothing.

### Types and kinds

| Type | Content |
| --- | --- |
| `persona` | Someone the software serves: `description`, `goals[]`, `capabilities[]`. |
| `requirement` | A rule the software follows: `statement` and, optionally, a `kind`. |

| Requirement kind | Meaning |
| --- | --- |
| `business-rule` | A rule of the domain, such as "an issued invoice is immutable". |
| `functional` | Something the software does for a persona. |
| `constraint` | A limit the software respects: legal, technical, operational. |

Adding a type later takes a new constant in the knowledge core, a row in this page, and a decision recorded in the implementation record of the epic that adds it.

### Support

| Field | Content |
| --- | --- |
| `run` | The run that produced it. |
| `agent` | The agent identifier within that run. |
| `interpretation` | The identifier of the interpretation in the agent report. |
| `statement` | The interpretation, as the agent wrote it. |
| `nature` | `supported`: needs at least one observation. `hypothesis`: an inference the agent declared as such; observations are optional. |
| `observations[]` | `statement` and `evidence[]`: at least one evidence identifier of the same item. |

A later run appends support; it never changes an existing entry.

### Evidence

| Field | Content |
| --- | --- |
| `id` | `e1`, `e2`, ... within the item. |
| `source` | `code`, `doc` or `human-context`. |
| `path` | Clean path relative to the repository root. |
| `commit` | Full hash of the commit the citation was made at. |
| `start_line`, `end_line` | 1-based, inclusive. |
| `excerpt` | The cited lines. |
| `sha256` | Hex SHA-256 of `excerpt`. |
| `state` | Set by Raun, never by an agent. See [Evidence states](#evidence-states). |
| `reason` | Why the evidence has no ground, see the table below. Required for `invalid` and `stale`, empty for `verified` and `relocated`. |
| `checked_at`, `checked_commit` | When and at which commit the state was last computed. |

### Open points

| Field | Content |
| --- | --- |
| `id` | `p1`, `p2`, ... within the item. |
| `kind` | `disagreement`, `uncertainty` or `question`. |
| `summary` | One line. |
| `raised_by` | Optional: an agent identifier, `lead`, `reconciler` for a conflict with existing content, or `raun` for a verification problem. |
| `positions[]` | One side of a disagreement, kept as written: `statement`, `run`, `agents[]`, `evidence[]`. A disagreement has at least two positions. A position names the run that wrote it, which may be an earlier run of the item when a run contradicts existing content. |
| `resolution` | Set by a human: `at`, `by`, `position` (0-based index of the retained position, optional), `note`. |

## Identifiers

An item identifier is the type, a dash and six characters drawn at random from `23456789abcdefghjkmnpqrstuvwxyz`, for example `requirement-8fz2mc`. The alphabet leaves out characters that read alike (`0`, `1`, `i`, `l`, `o`).

- An identifier is unique within its repository. The same identifier may exist in another repository.
- It never changes.
- Creating an item with an identifier already taken in the repository fails; the caller draws another one.

Evidence, open point and position identifiers are local to their item.

## Revisions

The revision of an item is an integer. It is `1` when the item is created and grows by one on every successful update. An update states the revision it read; if the stored revision differs, the update is refused and nothing changes. A human decision taken on a page that shows an older revision is therefore refused, not merged.

A revision says nothing about content: two updates that leave the content the same still give two revisions.

## Statuses

| Status | Meaning |
| --- | --- |
| `proposed` | Found by a run, waiting for a human decision. |
| `contested` | Found by a run, with at least one unresolved disagreement. |
| `validated` | Accepted by a human. |
| `rejected` | Refused by a human. |
| `needs-review` | Validated, but its ground changed since. |

| From | To | Who | Condition |
| --- | --- | --- | --- |
| (none) | `proposed` | run | Creation. |
| `proposed` | `contested` | run | The run adds a disagreement. |
| `proposed` | `validated`, `rejected` | human | |
| `contested` | `proposed` | human | Every disagreement is resolved. Recorded automatically when the last one is resolved: a human event with the reason `disagreements resolved`, whose `at` and `by` are those of that resolution. |
| `contested` | `rejected` | human | |
| `validated` | `needs-review` | run | An evidence of the item becomes `stale`, or the run adds a disagreement. |
| `validated` | `rejected` | human | |
| `needs-review` | `validated`, `rejected` | human | `validated` requires every disagreement to be resolved. |
| `rejected` | `proposed` | human | Reopening, when no disagreement is unresolved. |
| `rejected` | `contested` | human | Reopening, when a disagreement is unresolved. |

- A transition missing from the table is refused.
- A `contested` item is never validated directly: resolving its last disagreement makes it `proposed`, and a human validates it from there. raun-old listed `contested` to `validated`, but the automatic move made that row unreachable.
- A run never moves an item to `validated` or `rejected`.
- A run may append support, evidence and open points to an item in any status. The status changes only as the table says: a disagreement added to a `proposed` item makes it `contested`, to a `validated` item makes it `needs-review`, and changes nothing on a `contested`, `needs-review` or `rejected` item. A `rejected` item found again stays `rejected`.
- An item cannot be `validated` while one of its disagreements is unresolved. Uncertainties and questions do not block validation.
- A `proposed` item never holds an unresolved disagreement: a disagreement makes it `contested`, and reopening a `rejected` item that holds one makes it `contested`.
- Resolving the last disagreement moves an item automatically only from `contested`. A `needs-review` item stays `needs-review` until a human validates or rejects it.
- Support that only confirms an item changes no status, and neither does a run that does not find an item again.
- A human status change requires a reason. Resolving an open point requires a note; it may name the retained position. It never changes the title, the type-specific content, the support or the evidence of the item; only the status may change, as the table says.

## History

Every status change appends one event. Events are never changed or deleted, and they are in time order: a change dated before the last change of the item is refused.

| Field | Content |
| --- | --- |
| `at` | UTC. |
| `actor` | `human` or `run`. |
| `by` | For `human`, the account that decided. For `run`, the run identifier. |
| `from` | The previous status; absent on creation. |
| `to` | The new status. |
| `reason` | Required for `human`. For `run`, why the ground of the item changed: required on every event but the creation. |

Until accounts exist (epic 4), `by` is an opaque identifier supplied by the caller; it is never free text typed by the person who decides.

## Evidence states

Raun checks evidence without any LLM, against the content of the repository at a commit.

The excerpt must equal the cited lines, as whole lines. Before comparing, CRLF is read as LF, runs of spaces and tabs count as one space, the edges of each line are trimmed, and blank lines at both ends are ignored. A fragment of a line never matches. A citation longer than the maximum number of lines (40 by default) is invalid.

| State | Meaning |
| --- | --- |
| `verified` | Integrity: the excerpt is at the cited lines. Freshness: the excerpt still holds at the later commit, see below. |
| `relocated` | At the cited commit, the excerpt is at one other place in the file, and only one. Raun corrects the lines. |
| `invalid` | At the cited commit, the citation has no ground. |
| `stale` | It held at the cited commit, but not at a later one. |

| Reason | State | Meaning |
| --- | --- | --- |
| `not-found` | `invalid`, `stale` | The excerpt is nowhere in the file. |
| `ambiguous` | `invalid`, `stale` | The excerpt is not at the cited lines and appears at several other places; Raun does not pick one. |
| `missing-file` | `invalid`, `stale` | The file does not exist at that commit. |
| `not-regular` | `invalid`, `stale` | The path is a directory, a link or a submodule. |
| `binary` | `invalid`, `stale` | The file is not text. |
| `too-long` | `invalid` | The citation exceeds the maximum number of lines. |
| `bad-range` | `invalid` | At the cited commit, the line range is empty, reversed or outside the file. |

Two checks exist:

- **Integrity**, at the cited commit: does the citation hold where the agent made it? It gives `verified`, `relocated` or `invalid`.
- **Freshness**, at a later commit, usually the head of the default branch: does it still hold? It applies to evidence stored as `verified`, `relocated` (whose lines were already corrected at the cited commit) or `stale`, never to `invalid` evidence, which had no ground to begin with. It gives `verified` or `stale`:
  - the excerpt is at the stored lines, or at one other place only: `verified`;
  - it appears at several other places: `stale`, reason `ambiguous`;
  - it is nowhere in the file, including when the stored range no longer fits a shorter file: `stale`, reason `not-found`;
  - the file is missing, not regular or binary: `stale` with that reason.

  A `stale` evidence that holds again goes back to `verified`. When the excerpt is found at another place, the stored commit and lines stay as they are, and the evidence keeps verifying against them at its own commit; whether they move to the later commit is decided by the epic 7 review (#44).

## Format versions

| Format | Versioned by | Owner |
| --- | --- | --- |
| Database schema | The number of the last applied migration | Epic 2 (#14) |
| Knowledge export (YAML, Markdown) | A `version` field | Epic 11 |
| Repository configuration (`.raun.yaml`) | A `version` field | Epic 8 |
| Agent report contract | A `version` field | Epic 8 |
| Run manifest | A `version` field | Epic 10 |

Items are stored in the database; the JSON document of an item is an internal representation, changed only through a migration. A format read from outside, such as `.raun.yaml` or an agent report, with an unknown version is refused, never guessed.
