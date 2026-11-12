# Raun

Raun is Old Norse for "trial, test, experience". Í raun means "in reality".

Raun builds an evidence-backed knowledge base of a software project, such as its personas and business requirements. Several LLM agents analyze the repository independently. A lead agent consolidates their findings and keeps disagreements visible. Every claim points to exact lines at a given commit, and Raun checks each citation itself. Agents only propose knowledge; a human validates it.

Raun is self-hosted and connected to your forge, in the spirit of Drone CI: a server receives the forge events and hosts the review interface, and separate runners check the code and run the agents with your own LLM credentials.

## Status

Early development: the binary builds, its `server` and `runner` subcommands are stubs, and the knowledge model (`internal/core/knowledge`) exists with source classification, a reader of a commit on the `git` binary and deterministic evidence verification, without storage yet. The plan is in the [issues](https://github.com/kvitrvn/raun/issues) and the [implementation records](docs/implementation/README.md).

## Usage

```sh
raun server    # run the server (not implemented yet)
raun runner    # run a runner (not implemented yet)
raun version   # print the version
raun help      # print the help
```

## Development

Requirements: Go 1.27.3 and GNU Make. The linters are pinned in `go.mod` and run with `go tool`; nothing else is installed.

```sh
make build     # bin/raun
make verify    # the gate run before every change and by CI
```

Read [AGENTS.md](AGENTS.md) for the conventions and [docs/](docs/README.md) for the product, the architecture and the threat model.

## License

[MIT](LICENSE)
