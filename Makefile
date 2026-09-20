BIN ?= oc
ARGS ?=

.PHONY: build
build: ## Build the oc binary
	go build -o $(BIN) .

.PHONY: install
install: ## Install oc into GOBIN
	go install .

.PHONY: run
run: ## Run oc from source (pass flags with ARGS="-port 9090")
	go run . $(ARGS)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector
	go test -race ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "Unformatted files:"; echo "$$out"; exit 1; \
	fi

.PHONY: lint
lint: fmt-check vet ## Run all lint checks

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BIN)

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
