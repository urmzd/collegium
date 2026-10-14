.PHONY: all init build test lint fmt check dev web client client-check spec-lint oag-install

BIN := collegium

# The generator that writes packages/client from api/openapi.yaml, and the one
# version of it whose output is committed. CI regenerates with the same version
# and fails on any difference.
OAG ?= oag
OAG_VERSION := 0.20.6
# sha256 of each v$(OAG_VERSION) release asset, computed from the downloads.
# oag publishes no checksums (urmzd/oag#37), so they are pinned here and the
# download is refused on any mismatch.
OAG_SHA256_x86_64-unknown-linux-musl := 61ab4f13f49a32191ea3783ab05708cfd571192b7ea4c6f61420f4c53b10ae3e
OAG_SHA256_aarch64-unknown-linux-musl := 4e4610685960dd7d2885b11d24be5a6e70e39f61e9096fa72f93d522ce13fafe
OAG_SHA256_aarch64-apple-darwin := 347a29cde8f0a6e5be042ac8af2d17980c55a663e996deef3424a0337c7803a5
OAG_SHA256_x86_64-apple-darwin := f4a94758b6b3c4b955973349efde46ff1f5e41b19da2e133391f76d86ee1d701
OAG_DIR ?= $(HOME)/.local/bin
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

# The web console, proxying /api and /auth to the server from `make dev`.
web:
	cd apps/web && pnpm install && pnpm dev

# Regenerate the TypeScript client from the API contract. The output under
# packages/client/src/generated is committed.
client:
	@$(OAG) version | grep -qx 'oag v$(OAG_VERSION)' || { \
		echo "make client needs oag v$(OAG_VERSION), found: $$($(OAG) version 2>&1 | head -1)"; \
		echo "install it with: make oag-install, then put $(OAG_DIR) on PATH"; \
		exit 1; }
	cd packages/client && $(OAG) generate

# Build, typecheck and test the client package.
client-check:
	cd packages/client && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test

# Lint the API contract.
spec-lint:
	$(REDOCLY) lint api/openapi.yaml --config api/redocly.yaml

# Download the pinned oag release for this machine into $(OAG_DIR), checking
# its sha256 before it is made executable.
oag-install:
	@case "$$(uname -s)-$$(uname -m)" in \
		Linux-x86_64) target=x86_64-unknown-linux-musl ;; \
		Linux-aarch64) target=aarch64-unknown-linux-musl ;; \
		Darwin-arm64) target=aarch64-apple-darwin ;; \
		Darwin-x86_64) target=x86_64-apple-darwin ;; \
		*) echo "no pinned oag build for $$(uname -s)-$$(uname -m)"; exit 1 ;; \
	esac; \
	case "$$target" in \
		x86_64-unknown-linux-musl) want=$(OAG_SHA256_x86_64-unknown-linux-musl) ;; \
		aarch64-unknown-linux-musl) want=$(OAG_SHA256_aarch64-unknown-linux-musl) ;; \
		aarch64-apple-darwin) want=$(OAG_SHA256_aarch64-apple-darwin) ;; \
		x86_64-apple-darwin) want=$(OAG_SHA256_x86_64-apple-darwin) ;; \
	esac; \
	tmp=$$(mktemp); \
	curl -fsSL -o "$$tmp" "https://github.com/urmzd/oag/releases/download/v$(OAG_VERSION)/oag-$$target" || { rm -f "$$tmp"; exit 1; }; \
	got=$$( (command -v sha256sum >/dev/null && sha256sum "$$tmp" || shasum -a 256 "$$tmp") | cut -d' ' -f1); \
	if [ "$$got" != "$$want" ]; then echo "oag-$$target: sha256 $$got, want $$want"; rm -f "$$tmp"; exit 1; fi; \
	mkdir -p "$(OAG_DIR)" && mv "$$tmp" "$(OAG_DIR)/oag" && chmod +x "$(OAG_DIR)/oag"; \
	echo "installed oag v$(OAG_VERSION) ($$target, sha256 verified) to $(OAG_DIR)/oag"
