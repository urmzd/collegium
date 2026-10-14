.PHONY: all init build test lint fmt check dev dev-signin web client client-check spec-lint

BIN := collegium

# The generator that writes packages/client from api/openapi.yaml, and the one
# version of it whose output is committed. CI regenerates with the same version
# and fails on any difference.
OAG ?= oag
OAG_VERSION := 0.20.6
REDOCLY := npx --yes @redocly/cli@2.63.0

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

# Local preview with sign-in: a development identity provider on loopback
# (127.0.0.1:8081) and personas to pick from, no passwords. Use with `make web`
# and open http://127.0.0.1:5173/auth/login.
dev-signin: build
	./bin/$(BIN) serve -auth=mock -seed -public-url http://127.0.0.1:5173

# The web console, proxying /api and /auth to the server from `make dev`.
web:
	cd apps/web && pnpm install && pnpm dev

# Regenerate the TypeScript client from the API contract. The output under
# packages/client/src/generated is committed.
client:
	@$(OAG) version | grep -qx 'oag v$(OAG_VERSION)' || { \
		echo "make client needs oag v$(OAG_VERSION), found: $$($(OAG) version 2>&1 | head -1)"; \
		echo "install it with: curl -fsSL https://raw.githubusercontent.com/urmzd/oag/v$(OAG_VERSION)/install.sh | OAG_VERSION=v$(OAG_VERSION) sh"; \
		exit 1; }
	cd packages/client && $(OAG) generate

# Build, typecheck and test the client package.
client-check:
	cd packages/client && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test

# Lint the API contract.
spec-lint:
	$(REDOCLY) lint api/openapi.yaml --config api/redocly.yaml
