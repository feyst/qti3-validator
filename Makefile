# Development tasks. `make check` runs everything CI runs.

GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.8.0
GOBIN                 := $(shell go env GOPATH)/bin
IMAGE                 := feyst/qti3-validator
# The version a locally built image reports: dev-<commit>, with -dirty when
# the work tree differs from that commit, or dev without git.
DEV_VERSION           := $(or $(addprefix dev-,$(shell git rev-parse --short HEAD 2>/dev/null)),dev)$(if $(shell git status --porcelain 2>/dev/null),-dirty)
# The public 1EdTech QTI examples, pinned by commit; see CONTRIBUTING.md.
CORPUS_REPO           := https://github.com/1EdTech/qti-examples.git
CORPUS_COMMIT         := 0a92fbbb6d2e620a1f7fad19977be4c418246bc0
CORPUS_DIR            := testdata/corpus/qti-examples

# The code embeds the fetched schemas, so nothing compiles without them. The
# compiled rules are the last file `fetchschemas` writes and stand for all of
# them here, the element names in elements.json.gz included: the targets that
# compile the code depend on this file, which is made on a fresh clone and
# again when the lock file, the rules, the QTI versions or the code that
# compiles the rules and reads the element names change. Schemas already fetched are reused, so only the
# first time needs the network.
SCHEMAS_DIR           := internal/adapter/schemastore/schemas
SCHEMAS               := $(SCHEMAS_DIR)/schematron.json.gz
SCHEMAS_DEPS          := $(SCHEMAS_DIR)/schemas.lock $(wildcard rules/*.sch) \
                         $(filter-out %_test.go,$(wildcard cmd/fetchschemas/*.go internal/lib/schematron/*.go internal/lib/xpath/*.go internal/lib/xmlenc/*.go internal/lib/xsdelements/*.go internal/domain/qti/version.go))
# The files the lock file pins, as `fetchschemas` stores them, and the
# element names it writes beside the rules. When one is missing, as after
# removing their directory, or the element names alone, the compiled rules
# are made again whatever their age, which fetches and writes what is
# missing.
SCHEMA_FILES          := $(shell awk '!/^\#/ && NF == 2 { sub("^https://", "", $$2); print "$(SCHEMAS_DIR)/" $$2 ".gz" }' $(SCHEMAS_DIR)/schemas.lock) \
                         $(SCHEMAS_DIR)/elements.json.gz
ifneq ($(words $(wildcard $(SCHEMA_FILES))),$(words $(SCHEMA_FILES)))
SCHEMAS_DEPS          += FORCE
endif

# One recipe at a time, also under -j: `make -j schemas build` would run two
# `fetchschemas` writing the same files, and the checks share the Go cache.
.NOTPARALLEL:

.PHONY: help schemas build test race cover bench lint fmt vuln check image tools corpus FORCE

help: ## Show the targets
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

schemas: ## Fetch the pinned schemas, compile the Schematron rules and read the element names, also when they are up to date
	go run ./cmd/fetchschemas

$(SCHEMAS): $(SCHEMAS_DEPS)
	go run ./cmd/fetchschemas

FORCE:

build: $(SCHEMAS) ## Build the service binary
	CGO_ENABLED=0 go build -trimpath -o bin/qti-validator ./cmd/qti-validator

test: $(SCHEMAS) ## Run the tests
	go test ./...

race: $(SCHEMAS) ## Run the tests with the race detector
	go test -race ./...

cover: $(SCHEMAS) ## Run the tests with a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

bench: $(SCHEMAS) ## Run the benchmarks
	go test -run '^$$' -bench . -benchmem ./internal/app

lint: $(SCHEMAS) ## Run the linters, including the architecture rules
	$(GOBIN)/golangci-lint run ./...

fmt: ## Format the code
	$(GOBIN)/golangci-lint fmt ./...

vuln: $(SCHEMAS) ## Check dependencies for known vulnerabilities
	$(GOBIN)/govulncheck ./...

check: lint race vuln ## Everything CI runs

corpus: $(SCHEMAS) ## Fetch the public 1EdTech QTI examples and validate them all (go test -run Corpus)
	@if [ ! -d $(CORPUS_DIR)/.git ]; then git init -q $(CORPUS_DIR) && git -C $(CORPUS_DIR) remote add origin $(CORPUS_REPO); fi
	git -C $(CORPUS_DIR) fetch -q --depth 1 origin $(CORPUS_COMMIT)
	git -C $(CORPUS_DIR) checkout -q --detach $(CORPUS_COMMIT)
	go test -count=1 -run Corpus ./internal/feature

image: ## Build the Docker image, tagged feyst/qti3-validator:dev and :latest; it reports version dev-<commit>
	docker build --build-arg VERSION=$(DEV_VERSION) -t $(IMAGE):dev -t $(IMAGE):latest .

tools: ## Install the linters
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
