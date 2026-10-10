.PHONY: all init test lint fmt check

all: check

init:
	git config core.hooksPath .githooks
	go mod download && go mod tidy

test:
	go test ./... -race

lint:
	golangci-lint run

fmt:
	gofmt -w .

check: fmt lint test
