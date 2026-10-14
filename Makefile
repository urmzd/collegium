.PHONY: all init build test lint fmt check dev web

BIN := collegium

all: check

init:
	git config core.hooksPath .githooks
	go mod download && go mod tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BIN) ./cmd/$(BIN)

test:
	go test ./... -race

lint:
	golangci-lint run

fmt:
	gofmt -w .

check: fmt lint test

# Local preview: no sign-in, loopback only, two example agents.
dev: build
	./bin/$(BIN) serve -auth=dev -seed

# The web console, proxying /api and /auth to the server from `make dev`.
web:
	cd packages/adapter && pnpm install
	cd apps/web && pnpm install && pnpm dev
