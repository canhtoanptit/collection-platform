SHELL := /bin/bash
.DEFAULT_GOAL := help

REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
BIN := $(REPO_ROOT)/bin
export PATH := $(BIN):$(PATH)

GOLANGCI_LINT_VERSION := v2.13.1

# Pinned build tools live in tools/go.mod (tool directives); invoke via TOOL.
# GOWORK=off is required: tools/ is not a go.work member, so its tool
# directives are invisible in workspace mode.
TOOL := GOWORK=off go -C $(REPO_ROOT)/tools tool

# Every Go module except tools/ (tool pins only, excluded from the workspace).
GO_MODULES := $(shell find . -name go.mod -not -path './tools/*' -not -path './bin/*' -not -path '*/node_modules/*' -exec dirname {} \; | sort)

# ------------------------------------------------------------------ meta ----

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: bootstrap
bootstrap: $(BIN)/golangci-lint ## Install local tool binaries, sync workspace, download tool deps
	go work sync
	go -C tools mod download

$(BIN)/golangci-lint:
	@mkdir -p $(BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $(BIN) $(GOLANGCI_LINT_VERSION)

# ------------------------------------------------------------ go quality ----

.PHONY: lint
lint: ## golangci-lint over every Go module
	@for m in $(GO_MODULES); do echo "--- lint $$m"; (cd $$m && $(BIN)/golangci-lint run ./...) || exit 1; done

.PHONY: fmt
fmt: ## Apply formatters (gofumpt, goimports) to every Go module
	@for m in $(GO_MODULES); do (cd $$m && $(BIN)/golangci-lint fmt ./...) || exit 1; done

.PHONY: build-all
build-all: ## go build every Go module
	@for m in $(GO_MODULES); do echo "--- build $$m"; go -C $$m build ./... || exit 1; done

.PHONY: test-all
test-all: ## go test every Go module
	@for m in $(GO_MODULES); do echo "--- test $$m"; go -C $$m test ./... || exit 1; done

# ------------------------------------------------- delegation / contracts ----

.PHONY: verify
verify: ## Run a work package's verification script: make verify WP=OPS-1
ifndef WP
	$(error usage: make verify WP=<WP-ID>)
endif
	bash scripts/verify/$(WP).sh

.PHONY: ownership-check
ownership-check: ## Fail if the working tree touches paths outside a WP's ownership: make ownership-check WP=<id>
ifndef WP
	$(error usage: make ownership-check WP=<WP-ID>)
endif
	bash tools/check-ownership.sh $(WP)

.PHONY: contracts-check
contracts-check: ## Validate contract artefacts (minimal now; extended by CON-7)
	bash scripts/ci/contracts-check.sh

# --------------------------------------------------------- local runtime ----

.PHONY: compose-up
compose-up: ## Start the local dev stack (built in Phase 3, E2E-0)
	@test -f e2e/compose.yaml || { echo "e2e/compose.yaml not built yet (Phase 3, E2E-0)"; exit 1; }
	docker compose -f e2e/compose.yaml up -d --build

.PHONY: compose-down
compose-down: ## Stop the local dev stack and remove volumes
	@test -f e2e/compose.yaml || exit 0
	docker compose -f e2e/compose.yaml down -v

# ----------------------------------------------------------- infra (CI) -----

.PHONY: tf-plan
tf-plan: ## Plan a terraform stack: make tf-plan STACK=10-network ENV=dev
ifndef STACK
	$(error usage: make tf-plan STACK=<nn-name> ENV=dev)
endif
	@test -d infrastructure/terraform/stacks/$(STACK) || { echo "stack $(STACK) not built yet (Phase 2)"; exit 1; }
	terraform -chdir=infrastructure/terraform/stacks/$(STACK) init -backend-config=$(REPO_ROOT)/infrastructure/terraform/envs/$(ENV)/backend.hcl
	terraform -chdir=infrastructure/terraform/stacks/$(STACK) plan -var-file=$(REPO_ROOT)/infrastructure/terraform/envs/$(ENV)/common.tfvars
