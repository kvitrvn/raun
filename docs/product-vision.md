# Product vision

## What Raun does

Raun builds an evidence-backed knowledge base of a software project: its personas and its business requirements. Several LLM agents analyze the repository independently. A lead agent consolidates their findings and keeps disagreements visible. Every claim points to exact lines at a given commit, and Raun checks each citation itself. Agents only propose knowledge; a human validates it.

Raun is self-hosted and connected to the forge, in the spirit of Drone CI:

- an administrator runs one Raun server for an organization, and one or more runners;
- people log in with their forge account;
- a repository is activated in a workspace, and its forge events trigger Raun;
- every push and every pull request gets a deterministic check of the knowledge, with no LLM;
- LLM analysis runs on the default branch, sparingly, or on demand;
- the people who have access to the repository review the knowledge in the web interface.

## For whom

- **Maintainers and product owners** who want the knowledge of a project written down and kept true to the code.
- **Reviewers of a pull request**, who learn which validated knowledge the change makes stale.
- **Administrators** who host Raun for their organization, with their own runners and their own LLM credentials.

## Invariants

These rules define the product. No change may weaken them.

1. **No claim without evidence.** Evidence is a path, a line range and an excerpt at a commit. Raun verifies it deterministically, never through an LLM.
2. **An LLM proposes; it never establishes.** Only a human decision moves knowledge to `validated` or `rejected`. Validated content is never rewritten automatically.
3. **Disagreements are kept as they are.** Divergent findings are stored as separate positions, never averaged or dropped.
4. **Agents analyze independently.** They never see the knowledge base, the configuration or each other's output.
5. **The lead works on anonymized reports.** It groups and arbitrates; it never adds evidence and never discards an interpretation.
6. **The reconciler only matches.** On a rerun it matches new items to existing ones and sees their content, never agents, runs or statuses. A rerun appends; it never rewrites.
7. **Provider neutrality.** No LLM provider, model or vendor API is named or imported in code. Agents are reached through the agent contract.
8. **Raun never writes to the analyzed repository.** It reads code and publishes statuses and comments on the forge.
9. **The server never holds LLM credentials.** They stay on the runners.
10. **The repository is untrusted.** Its configuration holds no command, and pull requests from forks get the deterministic check only.
11. **Permissions come from the forge.** Read access means viewing, write access means deciding, and Raun never grants more than the forge does.

## What Raun does not do

- Review pull requests or write code. Raun reports knowledge made stale; it does not judge the change.
- Run without a forge. There is no local file-based mode; the server database is the source of truth, and YAML and Markdown are exports.
- Host several organizations with public sign-up or billing.
- Call an LLM API itself. Agents are commands that the runner starts in a sandbox.

## Origin

Raun is a rewrite of `kvitrvn/raun-old`, a local CLI with the same purpose. raun-old is the functional specification: its knowledge model, evidence rules, agent roles and invariants are carried over. Its `docs/upgrade.md` recorded the decisions behind the hosted design. The decisions of each epic are recorded in [implementation](implementation/README.md).
