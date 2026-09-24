# Everything runs in Docker, so only Docker (with Compose v2) is required.
# On Windows, run these from Git Bash or WSL, or copy the commands.

GO_IMAGE   := golang:1.26-alpine
GO_RUN     := docker run --rm -v "$(CURDIR):/src" -v bs-calendar_gomod:/go/pkg/mod -v bs-calendar_gocache:/root/.cache/go-build -w /src -e GOFLAGS=-buildvcs=false $(GO_IMAGE)
BASE_REF   ?= origin/main

.PHONY: help up down reset logs ps test test-unit smoke fmt vet lint-api breaking fuzz convert psql

help: ## Show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

up: ## Build and start postgres, bootstrap, api and worker
	docker compose up -d --build

down: ## Stop the stack (keeps data)
	docker compose down

reset: ## Stop the stack and DELETE all local data
	docker compose down -v

logs: ## Follow api and worker logs
	docker compose logs -f api worker

ps: ## Show service status
	docker compose ps -a

test: ## All Go tests, including the flow + contract test against Postgres
	docker compose --profile test run --rm test

test-unit: ## Go tests without a database (the flow test is skipped)
	$(GO_RUN) go test -count=1 ./...

smoke: ## Follow docs/api.md against the running stack
	docker compose --profile tools run --rm smoke

fmt: ## Format Go code
	$(GO_RUN) gofmt -w services fixtures api

vet: ## Static checks
	$(GO_RUN) sh -c 'test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1); go vet ./...'

lint-api: ## Lint api/openapi.yaml with Redocly (redocly.yaml)
	docker run --rm -v "$(CURDIR):/spec" -w /spec redocly/cli lint api/openapi.yaml

breaking: ## Fail if api/openapi.yaml breaks compatibility with $(BASE_REF)
	git show $(BASE_REF):api/openapi.yaml > .base-openapi.yaml
	docker run --rm -v "$(CURDIR):/spec" tufin/oasdiff breaking /spec/.base-openapi.yaml /spec/api/openapi.yaml --fail-on ERR; \
	  status=$$?; rm -f .base-openapi.yaml; exit $$status

fuzz: ## Fuzz the BS date parser for 60 seconds
	$(GO_RUN) go test -run XXX -fuzz FuzzParseAndConvertBS -fuzztime 60s ./services/calendar-api/internal/bscal/

convert: ## Offline conversion, e.g. make convert CAL=AD DATE=2026-09-24
	$(GO_RUN) go run ./services/calendar-api/cmd/calendar-api convert $(CAL) $(DATE)

psql: ## Open psql on the local database
	docker compose exec postgres psql -U calendar -d calendar
