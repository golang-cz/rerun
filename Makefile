.PHONY: help
help:
	@cat Makefile | grep '^[a-z]*[:]$$'

.PHONY: install
install:
	go install -ldflags "-X main.version=dev-$$(git rev-parse --short HEAD) -X main.buildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/rerun

.PHONY: build
build:
	@rm -rf ./bin/*
	@mkdir -p ./bin
	go build -o ./bin/rerun ./cmd/rerun

.PHONY: test
test:
	go test ./...

.PHONY: run
run:
	go run ./cmd/rerun --except bin '*.md' -- echo rerun
