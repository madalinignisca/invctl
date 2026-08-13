# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

SHELL := /bin/bash
BIN   := bin/invctl

# What this build calls itself.
#
# TAKEN FROM GIT, NEVER TYPED. A version somebody edits by hand is wrong for the
# window between the release and them remembering, and that window has no upper
# bound. `git describe` says v0.1.0 on the tag, v0.1.0-3-gabc1234 three commits
# later, and appends -dirty when the tree has uncommitted changes -- so a build
# can never quietly claim to be a release it is not.
#
# An untagged clone with no tags at all falls back to "dev", which is what the
# package defaults to anyway.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
# SOURCE_DATE_EPOCH is honoured so a reproducible build stays reproducible.
DATE    ?= $(shell date -u -d "@$${SOURCE_DATE_EPOCH:-$$(date +%s)}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)

VERSION_PKG := github.com/madalinignisca/invctl/internal/version
LDFLAGS     := -s -w \
	-X '$(VERSION_PKG).Version=$(VERSION)' \
	-X '$(VERSION_PKG).Commit=$(COMMIT)' \
	-X '$(VERSION_PKG).Date=$(DATE)'

# Tailwind standalone CLI: no Node runtime in the build pipeline. DaisyUI was
# considered and dropped -- it is an npm package that has to be on disk, which
# would drag a second runtime into the build for styling convenience. The
# component layer in web/src/app.css replaces it.
TAILWIND         := bin/tailwindcss
TAILWIND_VERSION := v4.3.3
TAILWIND_URL     := https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-linux-x64

# The demo runs on SQLite. Nothing in docker-compose.yml is needed to try it.
#
# It binds 0.0.0.0 so the demo is reachable from another machine, on 8088
# because 8080 is commonly already taken. That means the demo is exposed to
# whatever network this host is on, over plain HTTP, with a fixed password --
# fine for showing someone the tool, not fine for anything real. Set
# INV_LISTEN=127.0.0.1:8088 to keep it local.
export INV_DB_DRIVER    ?= sqlite
export INV_DB_DSN       ?= file:invctl.db?_txlock=immediate
export INV_LISTEN       ?= 0.0.0.0:8088
export INV_ADMIN_USERS  ?= admin
export INV_ADMIN_PASSWORD ?= demo-password
export INV_SEED         ?= true
# Presentation settings. Both are demo-only and neither is a default anywhere
# else: a real deployment gets no credentials it did not configure and no
# readings nobody sent.
#
# The tokens are throwaway and public by construction -- they are in a Makefile
# in the repository. That is fine for a laptop demo and would be a credential
# leak anywhere else, which is why INV_AGENT_TOKENS has no default in config.
export INV_SEED_OBSERVATIONS ?= true
export INV_AGENT_TOKENS ?= mon-prod:demo-token-prod-0000000000000000,mon-oob:demo-token-oob-00000000000000000
export INV_AGENT_SCOPES ?= mon-prod:prod|dev|transit,mon-oob:prod|dev|transit

PG_DSN := postgres://invctl:invctl@127.0.0.1:5433/invctl?sslmode=disable

# Which manual fragments describe code that has changed since they were written.
# See docs/manual/AGENT.md -- regenerate only what this names.
manual-stale:
	@./tools/manual-stale.sh

manual-stale-v:
	@./tools/manual-stale.sh -v

.PHONY: help manual-stale manual-stale-v
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: css ## Build the static binary
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/invctl

.PHONY: version
version: ## Print what a build here would call itself
	@echo "version $(VERSION)"
	@echo "commit  $(COMMIT)"
	@echo "date    $(DATE)"

$(TAILWIND):
	@mkdir -p bin
	curl -sSL -o $(TAILWIND) $(TAILWIND_URL)
	chmod +x $(TAILWIND)

.PHONY: css
css: $(TAILWIND) ## Rebuild the stylesheet
	$(TAILWIND) -i web/src/app.css -o web/static/app.css --minify

.PHONY: css-watch
css-watch: $(TAILWIND)
	$(TAILWIND) -i web/src/app.css -o web/static/app.css --watch

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

.PHONY: dev
dev: css ## Migrate, seed and run against SQLite with live template reload
	go run ./cmd/invctl -dev

.PHONY: demo
demo: clean-db dev ## Throw away the database and start a fresh demo

.PHONY: run-postgres
run-postgres: css compose-up ## Run against PostgreSQL instead of SQLite
	INV_DB_DRIVER=postgres INV_DB_DSN="$(PG_DSN)" go run ./cmd/invctl -dev

.PHONY: migrate
migrate: ## Apply migrations to $INV_DB_DSN and exit
	go run ./cmd/invctl -migrate

.PHONY: seed
seed: ## Load the demo estate and exit
	go run ./cmd/invctl -seed

# The retention prune (docs/AUDIT.md rule 10). Admin-invoked, never automatic,
# and never reachable from a handler -- so it lives here rather than in a timer
# inside the server.
#
# AS names the operator the run is recorded against; it is required, and it is
# resolved to an opaque app_user id before anything is written, because
# change_log.actor never holds a username. KEEP is a request, not a guarantee:
# anything resolving to an in_scope environment keeps at least 365 days
# whatever KEEP says, and the run says so when the floor applies.
#
# Run it with DRY=1 first. It is the only command in this repo that removes a
# fact.
KEEP ?= 365

.PHONY: prune-observed
prune-observed: ## Prune observed transitions older than KEEP days (AS=<user>, DRY=1 to preview)
	@test -n "$(AS)" || (echo "AS=<username> is required: the run records who asked for it"; exit 1)
	go run ./cmd/invctl -prune-observed -prune-keep-days $(KEEP) -prune-as $(AS) \
	  $(if $(DRY),-prune-dry-run,)

.PHONY: clean-db
clean-db:
	rm -f invctl.db invctl.db-wal invctl.db-shm

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

# THE TIMEOUT IS EXPLICIT HERE FOR THE REASON IT IS IN CI: Go's default is ten
# minutes and the store package alone takes longer than that once both engines
# are in play. A local run that dies on a timeout teaches somebody to reach for
# test-sqlite, which is the habit this whole target exists to prevent.
.PHONY: test
test: compose-up ## Run the full suite against both engines
	INV_TEST_POSTGRES_DSN="$(PG_DSN)" go test ./... -count=1 -timeout 30m

# SQLITE ONLY, AND IT IS NOT THE GATE. Useful while iterating on something that
# cannot be dialect-specific; useless as evidence that a change is finished.
# `make test` is what CI runs and what the definition of done means, and a
# change that passes here and fails there is the exact failure the dual-engine
# rule exists to catch -- which has happened, on a release tag, after a whole
# day of green SQLite runs.
.PHONY: test-sqlite
test-sqlite: ## Run against SQLite only — NOT the gate; see `make test`
	@echo "SQLite only. This is not the gate: run 'make test' before calling anything done."
	go test ./... -count=1 -timeout 20m

.PHONY: test-race
test-race: compose-up ## Run the suite with the race detector
	INV_TEST_POSTGRES_DSN="$(PG_DSN)" go test ./... -race -count=1 -timeout 60m

.PHONY: cover
cover: compose-up ## Report test coverage
	INV_TEST_POSTGRES_DSN="$(PG_DSN)" go test ./... -coverprofile=coverage.out -count=1
	go tool cover -func=coverage.out | tail -20

.PHONY: compose-up
compose-up:
	@docker compose up -d --wait postgres

.PHONY: compose-down
compose-down: ## Stop the supporting containers
	docker compose down -v

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------

.PHONY: lint
lint: ## gofmt, go vet, golangci-lint and govulncheck
	@echo "== gofmt =="
	@test -z "$$(gofmt -l cmd internal web)" || (gofmt -l cmd internal web; echo "run gofmt -w"; exit 1)
	@echo "== go vet =="
	go vet ./...
	@echo "== golangci-lint =="
	@# Missing tool is a FAILURE, not a note. The previous version printed an
	@# install hint and exited 0, so a machine without staticcheck reported a
	@# clean lint -- the same fail-open shape as the envBool bug this project
	@# fixed in config. A gate that passes when it did not run is worse than no
	@# gate, because it is believed.
	@command -v golangci-lint >/dev/null 2>&1 || \
	  (echo "golangci-lint is required: see 'make tools'"; exit 1)
	golangci-lint run ./...
	@echo "== govulncheck =="
	@command -v govulncheck >/dev/null 2>&1 || \
	  (echo "govulncheck is required: see 'make tools'"; exit 1)
	govulncheck ./...

.PHONY: tools
tools: ## Install the linters `make lint` requires
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: clean
clean: clean-db ## Remove build artefacts
	rm -rf bin coverage.out
