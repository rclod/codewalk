# codewalk development tasks.

BINARY      := codewalk
BIN_DIR     := bin
PKG         := github.com/rclod/codewalk
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X $(PKG)/internal/cli.Version=$(VERSION)
GOBIN       := $(shell go env GOPATH)/bin

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build ./bin/codewalk
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/codewalk

.PHONY: install
install: ## Install codewalk into $(GOBIN)
	go install -ldflags "$(LDFLAGS)" ./cmd/codewalk
	@echo "installed $(GOBIN)/$(BINARY)"

.PHONY: uninstall
uninstall: ## Remove the installed binary
	rm -f $(GOBIN)/$(BINARY)

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: race
race: ## Run the test suite with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run tests with a coverage summary
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format the source tree
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

.PHONY: fmt-check
fmt-check: ## Fail if any file is unformatted
	@unformatted=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	if [ -n "$$unformatted" ]; then \
		echo "unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: check
check: fmt-check vet test ## Everything CI runs

.PHONY: smoke
smoke: build ## Deterministic checks against the most recent walkthrough
	$(BIN_DIR)/$(BINARY) eval check latest

.PHONY: assets
assets: ## Refresh vendored browser assets
	scripts/fetch-web-vendor.sh

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN_DIR) coverage.out
