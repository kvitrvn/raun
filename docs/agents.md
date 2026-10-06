# Agents

Raun does not call LLM providers itself. An agent is a command that you configure. It receives a prompt and answers with a JSON report. Any tool that can read a prompt and print JSON can be an agent: a coding-agent CLI, a script calling a model API, or a human-written stub.

## Configuration

```yaml
language: en                 # language agents write knowledge in
analysis:
  quorum: 2
  agents:
    - id: alpha
      instructions: builtin:analyst   # or a file path, relative to the repository root
      runner:
        kind: command
        argv: ["my-agent", "--json"]
        timeout: 20m
```

`instructions` only replaces the job description. Raun always appends the project facts, the knowledge types and the output contract, so custom instructions cannot break the contract.

## Command protocol (`kind: command`)

| | |
|---|---|
| stdin | The full prompt (Markdown). |
| stdout | One JSON object: the report. Logs and Markdown fences around it are tolerated; Raun takes the last top-level JSON object. |
| stderr | Free; saved with the run's raw artifacts. |
| exit code | Must be 0. Anything else counts as a failure. |
| working directory | A disposable copy of the analyzed commit. |
| environment | Inherited, plus `RAUN_RUN_ID` and `RAUN_ROLE` (`analyst`). |
| timeout | `runner.timeout`; the whole process group is killed when it expires. |

The working directory is a plain copy of the commit, outside the repository. It contains no `.git`, no `.raun/knowledge/` and no `.raun/runs/`. Agents therefore cannot read the knowledge base or earlier conclusions, and they cannot modify the repository. Any file an agent changes in its copy is listed in the run manifest (`snapshot_changes`).

## Report contract (v1)

```json
{
  "version": 1,
  "observations": [
    {
      "id": "o1",
      "statement": "Update rejects issued invoices.",
      "evidence": [
        {"path": "billing/invoice.go", "start_line": 4, "end_line": 6,
         "excerpt": "if inv.Status == StatusIssued {\n\treturn ErrInvoiceLocked\n}"}
      ]
    }
  ],
  "interpretations": [
    {
      "id": "i1",
      "type": "requirement",
      "title": "Issued invoices are immutable",
      "statement": "Once issued, an invoice cannot be updated.",
      "nature": "supported",
      "observations": ["o1"],
      "requirement": {"statement": "An issued invoice cannot be updated.", "kind": "business-rule", "rationale": "", "personas": []}
    }
  ],
  "questions": [
    {"id": "q1", "question": "Can an admin cancel an issued invoice?", "about": ["i1"]}
  ]
}
```

- **Observations** are facts. Each one has at least one citation of whole lines (`path`, 1-based inclusive `start_line`/`end_line`, `excerpt`).
- **Interpretations** are candidate knowledge items. `type` is one of the configured types, and the matching content block (`persona` or `requirement`) is required. Fields are listed in [format.md](format.md). In a requirement, `personas` lists ids of persona interpretations from the same report.
- **`nature`**: `supported` needs at least one observation. `hypothesis` marks an inference and may have none.
- **Questions** are points the agent could not settle. `about` lists interpretation ids, or is empty for project-wide questions.
- Ids are unique across the report. Unknown fields are rejected.

An invalid report fails the agent, and the run continues if the quorum is still met. `raun run` prints every contract violation with its field path, for example `interpretations[1].requirement.kind: ...`.

## What Raun does with a report

1. **Classify each cited path.** `sources.context` files give `human-context`. Paths matching `sources.exclude` are rejected. Paths matching `sources.docs` give `doc`. Everything else is `code`.
2. **Verify each citation** at the analyzed commit, without any LLM (see [format.md](format.md#evidence-states)). Raun stores the file's own text, not the agent's copy of it.
3. **Drop invalid evidence.** An observation left without valid evidence is dropped. A `supported` interpretation that loses all its observations becomes a `hypothesis`. Both cases add an `uncertainty` open point that explains why.
4. **Create one `proposed` item per interpretation.** Each question becomes a `question` open point on the items it is about. Project-wide questions go to the run manifest.

Nothing an agent writes is ever `validated`. Only a human can validate.
