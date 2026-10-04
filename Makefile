# DevAssist developer commands. Run `make help` for the list.

SHELL := /bin/bash
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env
GO_MODULES := libs/gocommon services/indexer services/sandbox-runner
PY_PATHS := libs/devassist-common services/api services/orchestrator
export GOTOOLCHAIN := local

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.env:
	cp .env.example .env
	@echo "Created .env from .env.example"

# ---------------------------------------------------------------------------
# Stack
# ---------------------------------------------------------------------------

.PHONY: up
up: .env ## Build and start the whole stack, wait until every service is healthy
	$(COMPOSE) up -d --build --wait
	@$(MAKE) --no-print-directory ps

.PHONY: down
down: ## Stop the stack (keeps data volumes)
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete its data volumes
	$(COMPOSE) down -v

.PHONY: ps
ps: ## Show service status
	$(COMPOSE) ps --format 'table {{.Service}}\t{{.Status}}\t{{.Ports}}'

.PHONY: logs
logs: ## Tail logs from every service (make logs s=api for one)
	$(COMPOSE) logs -f $(s)

.PHONY: migrate
migrate: ## Apply database migrations to the running stack
	$(COMPOSE) run --rm migrate

.PHONY: smoke
smoke: ## Hit every service's readiness endpoint
	@for url in http://localhost:8000/readyz http://localhost:8001/readyz \
	            http://localhost:8080/readyz http://localhost:8081/readyz \
	            http://localhost:3000/api/readyz; do \
	  printf '%-36s ' $$url; body=$$(curl -fsS $$url) || { echo FAILED; exit 1; }; echo "$$body"; \
	done

# ---------------------------------------------------------------------------
# Dependencies
# ---------------------------------------------------------------------------

.PHONY: install
install: ## Install local toolchains' dependencies (uv, go, npm)
	uv sync --all-packages
	@for m in $(GO_MODULES); do (cd $$m && go mod download); done
	cd dashboard && npm ci

# ---------------------------------------------------------------------------
# Quality gates
# ---------------------------------------------------------------------------

.PHONY: test
test: test-python test-go test-dashboard ## Run every unit test suite

.PHONY: test-python
test-python:
	uv run pytest

.PHONY: test-go
test-go:
	@for m in $(GO_MODULES); do echo "==> $$m"; (cd $$m && go test -race ./...) || exit 1; done

.PHONY: test-dashboard
test-dashboard:
	cd dashboard && npm test

.PHONY: test-integration
test-integration: .env ## Integration tests against the running stack (make up first)
	uv run pytest -m integration

.PHONY: lint
lint: lint-python lint-go lint-dashboard ## Run every linter and type checker

.PHONY: lint-python
lint-python:
	uv run ruff check .
	uv run ruff format --check .
	uv run mypy $(addsuffix /src,$(PY_PATHS)) $(addsuffix /tests,$(PY_PATHS))

.PHONY: lint-go
lint-go:
	@for m in $(GO_MODULES); do echo "==> $$m"; \
	  (cd $$m && test -z "$$(gofmt -l .)" && go vet ./... && golangci-lint run --config $(CURDIR)/.golangci.yml ./...) || exit 1; \
	done

.PHONY: lint-dashboard
lint-dashboard:
	cd dashboard && npm run lint && npm run typecheck

.PHONY: fmt
fmt: ## Auto-format all code
	uv run ruff check --fix .
	uv run ruff format .
	@for m in $(GO_MODULES); do (cd $$m && gofmt -w .); done
