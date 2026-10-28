# Agents

Raun does not call LLM providers itself. An agent is a command that you configure. It receives a prompt and answers with a JSON report. Any tool that can read a prompt and print JSON can be an agent: a coding-agent CLI, a script calling a model API, or a human-written stub.

## Configuration

```yaml
version: 2
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
        max_output_bytes: 16777216  # default 16 MiB; 0 = no limit
        report_retries: 1           # default 1; 0 = never ask again
        report_grace: 2m            # default 2m; 0 = no retry after a timeout
    - id: beta
      runner: { kind: command, argv: ["my-other-agent"], timeout: 20m }
lead:                          # required with several analysts
  instructions: builtin:lead
  runner: { kind: command, argv: ["my-agent", "--json"], timeout: 20m }
```

The configuration format is version 2. Raun refuses a version 1 file: set `version: 2`, the rest of a version 1 file is still valid.

Analysts run in parallel and independently. When several reports are valid, the lead consolidates them. A run with a single valid report skips the lead and creates one item per interpretation. If fewer than `quorum` reports are valid, or if the lead fails, the run fails and writes nothing to the knowledge base.

`instructions` only replaces the job description. Raun always appends the project facts, the knowledge types and the output contract, so custom instructions cannot break the contract.

## Command protocol (`kind: command`)

| | |
|---|---|
| stdin | The full prompt (Markdown). |
| stdout | One JSON object: the report. Logs, Markdown fences and wrappers are tolerated (see [Reading the answer](#reading-the-answer)). At most `runner.max_output_bytes`: an agent that prints more is stopped and fails. |
| stderr | Free; saved with the run's raw artifacts. |
| exit code | Must be 0. Anything else counts as a failure. |
| working directory | A disposable copy of the analyzed commit. |
| environment | Inherited, plus `RAUN_RUN_ID` and `RAUN_ROLE` (`analyst` or `lead`). |
| timeout | `runner.timeout`; the whole process group is killed when it expires. What the agent printed before is still read (see [Missing answers](#missing-answers)). |

The working directory is a plain copy of the commit, outside the repository. It contains no `.git` and nothing from `.raun/` except the context files. Agents therefore cannot read the knowledge base, earlier conclusions, or the configuration that would tell them which agents take part. They cannot modify the repository either. Any file an agent changes in its copy is listed in the run manifest (`snapshot_changes`).


## Reading the answer

Raun reads an analyst's report and the lead's consolidation the same way:

- Every top-level JSON object in stdout is a candidate. Starting from the last one, the first that has a top-level `version` key is the answer.
- A candidate without `version` may carry the answer as text in one of its string values: a tool that wraps its final message in a JSON envelope, or a stream of JSON lines whose last message holds the report. Raun searches those strings the same way, a few levels deep. It knows no particular tool.
- A candidate that does not parse is repaired first. A backslash that starts no valid escape (`\d`, `C:\Users`) is doubled, and raw control characters inside strings are escaped.

The answer found is then decoded strictly and validated as usual. What Raun decoded is saved as `report.json` (or `consolidation.json` for the lead) with the raw artifacts.

## Missing answers

An analyst or the lead that leaves no valid answer is not always lost:

- **Stopped by its timeout.** Raun reads what the agent printed before it was stopped. If that holds a valid answer, it is kept with a warning, and the manifest marks the agent `salvaged: true`. It counts toward the quorum like any other report. If the answer was cut while it was being printed, Raun closes it after its last complete array element and validates the result. An element cut in its middle is dropped.
- **No valid answer, but something to build one from.** This covers an agent that exits with code 0 having answered in prose, stopped at "now let me write the JSON", or broken the contract, and an agent stopped by its timeout without a usable answer. Raun asks it again, at most `runner.report_retries` times. The retry runs on the same copy of the commit. Its prompt starts with `# Raun report retry` and holds:
  - the problems found, with their field paths;
  - the last 64 KiB of what the agent printed;
  - the original prompt from its `## Project` section on (project facts, knowledge types, the reports for the lead, and the output contract).

  The agent is told not to start over. A retry has the agent's `timeout`, or `report_grace` when the attempt before it was stopped by its timeout, since that timeout is already spent. `report_grace: 0` disables the retry after a timeout. A retry stopped by its timeout is salvaged in the same way.
- **Failed for good.** An agent that printed almost nothing (under 64 bytes) is not asked again: there is nothing to build an answer from, and the likely cause (a prompt over the model's context window, a refused API call) would not change. Neither is an agent that exits with a non-zero code or exceeds `max_output_bytes`. An interrupted run is not retried either.

The manifest counts the retries in `report_retries`. Retry prompts and outputs are kept with the raw artifacts as `prompt.retry<n>.md`, `stdout.retry<n>.txt` and `stderr.retry<n>.txt`.
## Analyst report contract (v1)

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

An invalid report fails the agent once its retries are spent (see [Missing answers](#missing-answers)), and the run continues if the quorum is still met. `raun run` prints every contract violation with its field path, for example `interpretations[1].requirement.kind: ...`.

## What Raun does with a report

1. **Classify each cited path.** `sources.context` files give `human-context`. Paths matching `sources.exclude` are rejected. Paths matching `sources.docs` give `doc`. Everything else is `code`.
2. **Verify each citation** at the analyzed commit, without any LLM (see [format.md](format.md#evidence-states)). Raun stores the file's own text, not the agent's copy of it.
3. **Drop invalid evidence.** An observation left without valid evidence is dropped. A `supported` interpretation that loses all its observations becomes a `hypothesis`. Both cases add an `uncertainty` open point that explains why.
4. **Create one `proposed` item per interpretation.** Each question becomes a `question` open point on the items it is about. Project-wide questions go to the run manifest.

Nothing an agent writes is ever `validated`. Only a human can validate.

## Lead consolidation contract (v1)

The lead receives the valid reports in its prompt, as JSON. They are anonymized: reports are labeled `A`, `B`, … in a random order, and every id is prefixed with its label (`A.i1`). Each citation carries Raun's verification outcome. The manifest records which label stands for which agent.

```json
{
  "version": 1,
  "items": [
    {
      "id": "k1",
      "type": "requirement",
      "title": "Issued invoice corrections",
      "requirement": {"statement": "...", "kind": "business-rule", "rationale": "", "personas": ["k2"]},
      "support": ["A.i2", "B.i2"],
      "disagreements": [
        {"summary": "Code forbids edits, docs allow 24 hours.",
         "positions": [
           {"statement": "Issued invoices are never editable.", "support": ["A.i2"]},
           {"statement": "Admins can edit within 24 hours.", "support": ["B.i2"]}
         ]}
      ],
      "uncertainties": ["..."],
      "questions": ["..."]
    }
  ]
}
```

The lead groups and arbitrates. It never adds evidence and never discards an interpretation. Raun checks that:

- every interpretation appears in the `support` of exactly one item, of the same type;
- a disagreement has at least two positions, each backed by interpretations of the item's own support, and no interpretation backs two positions;
- `personas` link to persona items of the consolidation;
- the contract has no field for evidence; unknown fields are rejected.

Raun then builds each item from its supporting interpretations, using only verified evidence. It derives each position's agents and evidence from the interpretations behind it, so the lead cannot misattribute them. The same question asked by several agents is recorded once. An item with a disagreement is created `contested`. Open points carry `raised_by`: the agent ID, `lead`, or `raun` for verification problems.
