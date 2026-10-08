# Development tasks. `make check` runs everything CI runs.

GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.8.0
GOBIN                 := $(shell go env GOPATH)/bin
IMAGE                 := feyst/qti3-validator
# The public 1EdTech QTI examples, pinned by commit; see CONTRIBUTING.md.
CORPUS_REPO           := https://github.com/1EdTech/qti-examples.git
CORPUS_COMMIT         := 0a92fbbb6d2e620a1f7fad19977be4c418246bc0
CORPUS_DIR            := testdata/corpus/qti-examples

.PHONY: help schemas build test race cover bench lint fmt vuln check image tools corpus

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

corpus: ## Fetch the public 1EdTech QTI examples and validate them all (go test -run Corpus)
	@if [ ! -d $(CORPUS_DIR)/.git ]; then git init -q $(CORPUS_DIR) && git -C $(CORPUS_DIR) remote add origin $(CORPUS_REPO); fi
	git -C $(CORPUS_DIR) fetch -q --depth 1 origin $(CORPUS_COMMIT)
	git -C $(CORPUS_DIR) checkout -q --detach $(CORPUS_COMMIT)
	go test -count=1 -run Corpus ./internal/feature

image: ## Build the Docker image, tagged feyst/qti3-validator:dev and :latest
	docker build -t $(IMAGE):dev -t $(IMAGE):latest .

tools: ## Install the linters
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
