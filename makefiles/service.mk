# Canonical targets for every Go service. Include from a service Makefile:
#
#   SERVICE := case
#   OPENAPI_SPEC := ../../contracts/openapi/case.v1.yaml   # optional override
#   include ../../makefiles/service.mk
#
# The service directory must follow the exemplar layout (docs/service-playbook.md).

REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)
BIN := $(REPO_ROOT)/bin
# GOWORK=off: tools/ is not a go.work member; its tool directives are
# invisible in workspace mode.
TOOL := GOWORK=off go -C $(REPO_ROOT)/tools tool
SERVICE ?= $(notdir $(CURDIR))
IMAGE ?= colx/$(SERVICE)
export PATH := $(BIN):$(PATH)

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Regenerate oapi-codegen server/types and sqlc queries (committed; CI diffs)
	@if [ -f api/oapi-codegen.yaml ]; then $(TOOL) oapi-codegen -config api/oapi-codegen.yaml $(OPENAPI_SPEC); fi
	@if [ -f sqlc.yaml ]; then $(TOOL) sqlc generate; fi

.PHONY: build
build: ## Build all packages
	go build ./...

.PHONY: lint
lint: ## golangci-lint this module
	$(BIN)/golangci-lint run ./...

.PHONY: test
test: ## Unit tests with race detector and coverage profile
	go test ./... -race -count=1 -coverprofile=coverage.out -covermode=atomic

.PHONY: coverage
coverage: test ## Enforce coverage floors (domain >=90%, module >=80%)
	$(TOOL) go-test-coverage --config .go-test-coverage.yml

.PHONY: test-integration
test-integration: ## Integration tests (testcontainers: postgres + redpanda; needs Docker)
	go test ./tests/... -tags integration -count=1 -timeout 15m

.PHONY: contract-test
contract-test: ## Handler tests validated against the OpenAPI contract
	go test ./... -run 'TestContract' -count=1

.PHONY: migrate-up
migrate-up: ## Apply migrations against $$DATABASE_URL
	go run ./cmd/server migrate up

.PHONY: run
run: ## Run the service locally
	go run ./cmd/server serve

.PHONY: image
image: ## Build the container image (distroless; binary built on host)
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o dist/server ./cmd/server
	docker build -t $(IMAGE):dev .
