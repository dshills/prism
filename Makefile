BINARY := prism
PKG    := ./cmd/prism
GOBIN  ?= $(shell go env GOPATH)/bin

.DEFAULT_GOAL := help

.PHONY: build install lint test race help

build: ## Build the prism binary into ./bin
	@mkdir -p bin
	go build -o bin/$(BINARY) $(PKG)

install: ## Install the prism binary to $GOBIN
	go install $(PKG)

lint: ## Run golangci-lint across all packages
	golangci-lint run ./...

test: ## Run the full test suite
	go test ./...

race: ## Run tests with the race detector enabled
	go test -race ./...

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
