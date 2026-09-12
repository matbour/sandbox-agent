# The repository lives on the board, in ~/sandbox-agent. Builds are native (linux/arm64).
# Go is installed in the home directory (~/.local/go): use the one on PATH, else that one.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)

# Keep this path stable: the AppArmor userns profile (M2, docs/host.md) will be attached to it.
BIN := bin/sandbox

# Run with `go run` at a pinned version rather than as a go.mod tool: golangci-lint's many
# dependencies would otherwise share our module graph and could bump our own versions.
# CI pins the same version in .github/workflows/ci.yml: change both together.
GOLANGCI_LINT := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

.PHONY: build test lint fmt check hooks generate linear-schema clean

build:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -o $(BIN) ./cmd/sandbox
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -o bin/agentd ./cmd/agentd

# Regenerate the Linear client (internal/linear/generated.go) from queries.graphql.
generate:
	cd internal/linear && $(GO) tool genqlient

# Refresh Linear's schema from the official SDK repository, pinned to the current commit.
linear-schema:
	sha=$$(git ls-remote https://github.com/linear/linear refs/heads/master | cut -f1) && \
	curl -sfL -o internal/linear/schema.graphql https://raw.githubusercontent.com/linear/linear/$$sha/packages/sdk/src/schema.graphql && \
	echo $$sha > internal/linear/schema.version

test:
	CGO_ENABLED=0 $(GO) test ./...

# go vet, the standard linters, and gofmt/goimports checks (.golangci.yml).
lint:
	$(GOLANGCI_LINT) run

# Rewrite files with gofmt and goimports.
fmt:
	$(GOLANGCI_LINT) fmt

check: lint test build

# Install the Git hooks defined in lefthook.yml.
hooks:
	$(GO) tool lefthook install

clean:
	rm -rf bin
