COMPOSE_FILE ?= compose.yml
CGO_ENABLED ?= 0
VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
ARCH ?= $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
PASS_PROGRAM_TARGETS = $(if $(filter undefined,$(origin PROGRAM_TARGETS)),,PROGRAM_TARGETS=$(PROGRAM_TARGETS))
PASS_PROGRAM_TARGET_MATRIX = $(if $(filter undefined,$(origin PROGRAM_TARGET_MATRIX)),,PROGRAM_TARGET_MATRIX=$(PROGRAM_TARGET_MATRIX))

ifeq ($(OS),Windows_NT)
SHELL := powershell.exe
.SHELLFLAGS := -NoProfile -Command
endif

.PHONY: run test test-integration docker-build docker-up docker-down release release-program clean

ifeq ($(OS),Windows_NT)
run:
	@Get-Content .env -ErrorAction SilentlyContinue | ForEach-Object { $$l = $$_.Trim(); if ($$l -and -not $$l.StartsWith('#')) { $$i = $$l.IndexOf('='); if ($$i -gt 0) { [System.Environment]::SetEnvironmentVariable($$l.Substring(0,$$i).Trim(), $$l.Substring($$i+1).Trim(), 'Process') } } }; $$env:SERVER_PORT = if ($$env:HOST_PORT) { $$env:HOST_PORT } else { if ($$env:SERVER_PORT) { $$env:SERVER_PORT } else { '8080' } }; $$env:CGO_ENABLED = '$(CGO_ENABLED)'; go run ./cmd/agent-platform-runner
else
run:
	set -a; [ ! -f .env ] || . ./.env; set +a; SERVER_PORT="$${HOST_PORT:-$${SERVER_PORT:-8080}}" CGO_ENABLED=$(CGO_ENABLED) go run ./cmd/agent-platform-runner
endif

test:
	@for pkg in $$(go list ./...); do \
		attempt=1; \
		while true; do \
			cache_dir=$$(mktemp -d); \
			GOCACHE=$$cache_dir CGO_ENABLED=$(CGO_ENABLED) go test $$pkg; \
			status=$$?; \
			if [ $$status -eq 0 ]; then \
				rm -rf $$cache_dir; \
				break; \
			fi; \
			rm -rf $$cache_dir; \
			if [ $$attempt -ge 3 ]; then \
				exit $$status; \
			fi; \
			echo "retrying $$pkg (attempt $$((attempt + 1))/3)"; \
			attempt=$$((attempt + 1)); \
		done; \
	done

test-integration:
	GOCACHE=$$(mktemp -d) CGO_ENABLED=$(CGO_ENABLED) RUN_SOCKET_TESTS=1 go test -p 1 -run TestQueryStreamsBeforeRunCompleteOverHTTP -v ./internal/server

docker-build:
	docker compose -f $(COMPOSE_FILE) build

docker-up:
	docker compose -f $(COMPOSE_FILE) up -d --build

docker-down:
	docker compose -f $(COMPOSE_FILE) down

release:
	$(MAKE) release-program VERSION=$(VERSION) ARCH=$(ARCH) $(PASS_PROGRAM_TARGETS) $(PASS_PROGRAM_TARGET_MATRIX)

release-program:
	VERSION=$(VERSION) ARCH=$(ARCH) $(PASS_PROGRAM_TARGETS) $(PASS_PROGRAM_TARGET_MATRIX) bash scripts/release-program.sh

clean:
	rm -rf dist/release
