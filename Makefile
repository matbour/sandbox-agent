# The repository lives on the board, in ~/sandbox-agent. Builds are native (linux/arm64).
# Go is installed in the home directory (~/.local/go): use the one on PATH, else that one.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)

# Keep this path stable: the AppArmor userns profile (M2, docs/host.md) will be attached to it.
BIN := bin/sandbox

.PHONY: build test generate linear-schema clean

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

clean:
	rm -rf bin
