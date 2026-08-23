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
contracts-check: ## Full contract gate: harness, contractcheck, vacuum, immutability, oasdiff
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

# ============================================================================ #
# INF-B — cost, teardown and port-forward targets (plan FND-13, ADR-0010)      #
#                                                                              #
# APPEND-ONLY SECTION. Everything above this banner belongs to other WPs; add   #
# below, never edit above.                                                      #
#                                                                              #
# Every target here talks to real, metered AWS. They are all guarded on         #
# `command -v aws` and fail with an explicit "needs credentials" message rather #
# than an AWS stack trace — no agent session has credentials (ADR-0010), so the #
# common case for these targets is "run by a human, later".                     #
#                                                                              #
# Cost levers (docs/cost-model.md):                                            #
#   everything running  ~$540-575/mo                                            #
#   make stop           ~$230/mo   back in minutes                              #
#   make destroy-heavy   ~$60/mo   back in ~60 min                              #
#   full destroy          <$5/mo   back in ~60 min                              #
# ============================================================================ #

CLUSTER_NAME ?= colx-dev
AWS_REGION   ?= eu-west-1
PLATFORM_NS  ?= platform
AIRFLOW_NS   ?= airflow
INGESTION_NS ?= ingestion

# Guard used by every target below. `aws` present is necessary, not sufficient —
# the scripts also check `sts get-caller-identity`.
define require_aws
	@command -v aws >/dev/null 2>&1 || { \
		echo "ERROR: the AWS CLI is not installed."; \
		echo "       This target changes real, metered infrastructure and needs AWS"; \
		echo "       credentials. Install AWS CLI v2 and log in (aws sso login), then"; \
		echo "       re-run. Nothing in this repo ships credentials (ADR-0010)."; \
		exit 1; }
endef

define require_kubectl
	@command -v kubectl >/dev/null 2>&1 || { \
		echo "ERROR: kubectl is not installed — needed to reach the cluster."; \
		exit 1; }
endef

# ------------------------------------------------------------- cost levers ----

.PHONY: stop
stop: ## Cost lever: node group -> 0 + stop both RDS (~$230/mo). Back in minutes
	$(require_aws)
	bash scripts/cost/stop.sh

.PHONY: start
start: ## Reverse of stop: RDS first (waited), then nodes (waited until Ready)
	$(require_aws)
	bash scripts/cost/start.sh

.PHONY: cost-report
cost-report: ## Cost Explorer by stack tag + Snowflake credits -> markdown: make cost-report MONTH=2026-08
	$(require_aws)
	bash scripts/cost/report.sh $(if $(MONTH),--month $(MONTH),)

.PHONY: destroy-heavy
destroy-heavy: ## Cost lever: destroy 30-eks + MSK in 20-data (~$60/mo). Rebuild ~60 min
	$(require_aws)
	@echo "This destroys the EKS cluster and the MSK brokers."
	@echo "Everything is declarative: 'make up-all' rebuilds it in ~60 minutes."
	@echo "SURVIVES: S3 (raw/archive versioned), RDS, KMS, ECR, Cognito/Keycloak DB, tfstate."
	@echo "LOST:     every pod, every Helm release, MSK topic data (topics are re-applied)."
	@echo
	@printf 'Type the cluster name (%s) to continue: ' "$(CLUSTER_NAME)"
	@read -r ans; [ "$$ans" = "$(CLUSTER_NAME)" ] || { echo "aborted"; exit 1; }
	terraform -chdir=infrastructure/terraform/stacks/30-eks init \
		-backend-config=$(REPO_ROOT)/infrastructure/terraform/envs/dev/backend.hcl \
		-backend-config=key=stacks/30-eks.tfstate
	terraform -chdir=infrastructure/terraform/stacks/30-eks destroy \
		-var-file=$(REPO_ROOT)/infrastructure/terraform/envs/dev/common.tfvars
	@# MSK only, by -target. Destroying all of 20-data would take the RDS
	@# instances, the buckets and the CMKs with it — which is a different lever
	@# (full destroy), not this one.
	terraform -chdir=infrastructure/terraform/stacks/20-data init \
		-backend-config=$(REPO_ROOT)/infrastructure/terraform/envs/dev/backend.hcl \
		-backend-config=key=stacks/20-data.tfstate
	terraform -chdir=infrastructure/terraform/stacks/20-data destroy \
		-target=module.msk \
		-var-file=$(REPO_ROOT)/infrastructure/terraform/envs/dev/common.tfvars

.PHONY: up-all
up-all: ## Rebuild everything: stacks in order, helmfile, topics, smoke (~60 min)
	$(require_aws)
	$(require_kubectl)
	@echo "=== 1/6  terraform: 10-network -> 20-data -> 30-eks"
	@echo "    Applies run in CI only (ADR-0010). This target plans and then tells"
	@echo "    you to merge; it deliberately does not apply from a laptop."
	for s in 10-network 20-data 30-eks; do \
		$(MAKE) tf-plan STACK=$$s ENV=dev || exit 1; \
	done
	@echo
	@echo "=== 2/6  kubeconfig"
	aws eks update-kubeconfig --name $(CLUSTER_NAME) --region $(AWS_REGION)
	@echo
	@echo "=== 3/6  namespaces"
	kubectl apply -f deployment/namespaces.yaml
	@echo
	@echo "=== 4/6  helmfile apply (external-secrets first, then the rest)"
	@command -v helmfile >/dev/null 2>&1 || { \
		echo "ERROR: helmfile is not installed. Install the pinned release"; \
		echo "       (see scripts/verify/INF-B.sh for the version and URL)."; exit 1; }
	helmfile -f deployment/helmfile.yaml -e dev \
		--state-values-set accountId=$$(aws sts get-caller-identity --query Account --output text) \
		--state-values-set registry=$$(aws sts get-caller-identity --query Account --output text).dkr.ecr.$(AWS_REGION).amazonaws.com \
		--state-values-set platformDbHost=$$(aws rds describe-db-instances --db-instance-identifier colx-dev-platform --query 'DBInstances[0].Endpoint.Address' --output text) \
		apply
	@echo
	@echo "=== 5/6  kafka topics (idempotent Job — INF-A owns deployment/kafka)"
	@test -f deployment/kafka/topics.yaml || { \
		echo "    deployment/kafka/topics.yaml not present yet (INF-A) — skipping"; }
	@test ! -f deployment/kafka/topics.yaml || kubectl apply -f deployment/kafka/
	@echo
	@echo "=== 6/6  smoke"
	kubectl get nodes
	kubectl get pods -A | grep -Ev 'Running|Completed' || echo "    all pods Running/Completed"
	@echo
	@echo "REMINDER: fill in the MSK broker targets in"
	@echo "  deployment/values/kube-prometheus-stack/dev.yaml (additionalScrapeConfigs)"
	@echo "  and re-apply — they are empty until the brokers exist."

# --------------------------------------------------- port-forward helpers -----
# There is no ingress until Phase 12 (ADR-0011). These are the front door.

.PHONY: grafana
grafana: ## Port-forward Grafana -> http://localhost:3000 (admin creds from Secrets Manager)
	$(require_kubectl)
	@echo "Grafana: http://localhost:3000"
	@echo "  user/password: aws secretsmanager get-secret-value --secret-id colx/dev/grafana/admin"
	kubectl -n $(PLATFORM_NS) port-forward svc/kube-prometheus-stack-grafana 3000:80

.PHONY: airflow
airflow: ## Port-forward the Airflow webserver -> http://localhost:8080
	$(require_kubectl)
	@echo "Airflow: http://localhost:8080"
	@echo "  user/password: aws secretsmanager get-secret-value --secret-id colx/dev/airflow/webserver"
	kubectl -n $(AIRFLOW_NS) port-forward svc/airflow-webserver 8080:8080

.PHONY: keycloak
keycloak: ## Port-forward Keycloak -> http://localhost:8081 (realm colx)
	$(require_kubectl)
	@echo "Keycloak: http://localhost:8081"
	@echo "  admin console: http://localhost:8081/admin"
	@echo "  realm issuer:  http://localhost:8081/realms/colx"
	@echo "  admin creds:   aws secretsmanager get-secret-value --secret-id colx/dev/keycloak/admin"
	kubectl -n $(PLATFORM_NS) port-forward svc/keycloak-http 8081:80

.PHONY: prometheus
prometheus: ## Port-forward Prometheus -> http://localhost:9090
	$(require_kubectl)
	kubectl -n $(PLATFORM_NS) port-forward svc/kube-prometheus-stack-prometheus 9090:9090

.PHONY: pf-cp
pf-cp: ## Port-forward the ingestion control plane -> http://localhost:8090 (from ING-2)
	$(require_kubectl)
	@kubectl -n $(INGESTION_NS) get svc ingestion-cp >/dev/null 2>&1 || { \
		echo "ingestion-cp service not deployed yet (built in Phase 5, ING-2)"; exit 1; }
	kubectl -n $(INGESTION_NS) port-forward svc/ingestion-cp 8090:8080
