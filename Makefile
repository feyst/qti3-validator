# Development tasks. `make check` runs everything CI runs.

GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.8.0
GOBIN                 := $(shell go env GOPATH)/bin

.PHONY: help schemas build test race cover bench lint fmt vuln check image tools

help: ## Show the targets
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

schemas: ## Fetch the pinned schemas and compile the Schematron rules
	go run ./cmd/fetchschemas

build: schemas ## Build the service binary
	CGO_ENABLED=0 go build -trimpath -o bin/qti-validator ./cmd/qti-validator

test: ## Run the tests
	go test ./...

race: ## Run the tests with the race detector
	go test -race ./...

cover: ## Run the tests with a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

bench: ## Run the benchmarks
	go test -run '^$$' -bench . -benchmem ./internal/app

lint: ## Run the linters, including the architecture rules
	$(GOBIN)/golangci-lint run ./...

fmt: ## Format the code
	$(GOBIN)/golangci-lint fmt ./...

vuln: ## Check dependencies for known vulnerabilities
	$(GOBIN)/govulncheck ./...

check: lint race vuln ## Everything CI runs

image: ## Build the Docker image
	docker build -t qti-validator:dev .

tools: ## Install the linters
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
