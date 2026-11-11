SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

GOCACHE ?= $(CURDIR)/.tmp/go-build
STATICCHECK_CACHE ?= $(CURDIR)/.tmp/staticcheck
GO := env GOCACHE=$(GOCACHE) go
BINARY := bin/raun

.PHONY: fmt fmt-check tidy-check vet lint architecture vuln test build commits verify

# Rewrites the Go files in place. CI runs fmt-check, which never rewrites.
fmt:
	$(GO) fmt ./...

fmt-check:
	@unformatted="$$(find . \( -path ./.tmp -o -path ./.git \) -prune -o -name '*.go' -print0 | xargs -0 -r gofmt -l)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi

tidy-check:
	$(GO) mod tidy -diff

vet:
	$(GO) vet ./...

lint:
	env GOCACHE=$(GOCACHE) STATICCHECK_CACHE=$(STATICCHECK_CACHE) go tool staticcheck ./...

architecture:
	./scripts/check-architecture.sh

# Needs the network to read the vulnerability database, so it is not part of verify.
vuln:
	$(GO) tool govulncheck ./...

test:
	$(GO) test ./...

build:
	mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BINARY) ./cmd/raun

# Checks the commit subjects between BASE and HEAD, origin/main by default.
BASE ?= origin/main
commits:
	./scripts/check-commits.sh "$(BASE)"

verify: fmt-check tidy-check vet lint architecture test build
