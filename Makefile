# The repository lives on the board, in ~/sandbox-agent. Builds are native (linux/arm64).
# Go is installed in the home directory (~/.local/go): use the one on PATH, else that one.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)

# Keep this path stable: the AppArmor userns profile (M2, docs/host.md) will be attached to it.
BIN := bin/sandbox

.PHONY: build test clean

build:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -o $(BIN) ./cmd/sandbox
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -o bin/agentd ./cmd/agentd

test:
	CGO_ENABLED=0 $(GO) test ./...

clean:
	rm -rf bin
