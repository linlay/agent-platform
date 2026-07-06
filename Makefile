COMPOSE_FILE ?= compose.yml
CGO_ENABLED ?= 0
VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
LOCAL_RELEASE_ROOT ?= release-local

# ARCH detection: use uname on Unix, default to amd64 on Windows
ARCH_DETECT := $(shell command -v uname >/dev/null 2>&1 && uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/' || echo "amd64")
ARCH ?= $(ARCH_DETECT)

PASS_PROGRAM_TARGETS = $(if $(filter undefined,$(origin PROGRAM_TARGETS)),,PROGRAM_TARGETS=$(PROGRAM_TARGETS))
PASS_PROGRAM_TARGET_MATRIX = $(if $(filter undefined,$(origin PROGRAM_TARGET_MATRIX)),,PROGRAM_TARGET_MATRIX=$(PROGRAM_TARGET_MATRIX))

ifeq ($(OS),Windows_NT)
SHELL := powershell.exe
.SHELLFLAGS := -NoProfile -Command
LOCAL_BINARY := agent-platform.exe
else
LOCAL_BINARY := agent-platform
endif

LOCAL_BACKEND_DIR := $(LOCAL_RELEASE_ROOT)/backend
LOCAL_BACKEND_BIN := $(LOCAL_BACKEND_DIR)/$(LOCAL_BINARY)
LOCAL_PLUGINS_DIR := $(LOCAL_RELEASE_ROOT)/plugins

.PHONY: run build-local run-local test test-integration docker-build docker-up docker-down release release-program clean

ifeq ($(OS),Windows_NT)
run: run-local

build-local:
	@New-Item -ItemType Directory -Path '$(LOCAL_BACKEND_DIR)' -Force | Out-Null; New-Item -ItemType Directory -Path '$(LOCAL_PLUGINS_DIR)' -Force | Out-Null; $$env:CGO_ENABLED = '$(CGO_ENABLED)'; go build -o '$(LOCAL_BACKEND_BIN)' ./cmd/agent-platform

run-local: build-local
	@Get-Content .env -ErrorAction SilentlyContinue | ForEach-Object { $$l = $$_.Trim(); if ($$l -and -not $$l.StartsWith('#')) { $$i = $$l.IndexOf('='); if ($$i -gt 0) { [System.Environment]::SetEnvironmentVariable($$l.Substring(0,$$i).Trim(), $$l.Substring($$i+1).Trim(), 'Process') } } }; if ([string]::IsNullOrWhiteSpace($$env:SERVER_PORT)) { $$env:SERVER_PORT = '11949' }; & '$(LOCAL_BACKEND_BIN)' --config-dir .
else
run: run-local

build-local:
	mkdir -p "$(LOCAL_BACKEND_DIR)" "$(LOCAL_PLUGINS_DIR)"
	CGO_ENABLED=$(CGO_ENABLED) go build -o "$(LOCAL_BACKEND_BIN)" ./cmd/agent-platform

run-local: build-local
	set -a; [ ! -f .env ] || . ./.env; set +a; SERVER_PORT="$${SERVER_PORT:-11949}" "$(LOCAL_BACKEND_BIN)" --config-dir .
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

ifeq ($(OS),Windows_NT)
release-program:
	powershell -ExecutionPolicy Bypass -File scripts/release-program.ps1
else
release-program:
	VERSION=$(VERSION) ARCH=$(ARCH) $(PASS_PROGRAM_TARGETS) $(PASS_PROGRAM_TARGET_MATRIX) bash scripts/release-program.sh
endif

clean:
	rm -rf dist/release
