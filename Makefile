COMPOSE_FILE ?= compose.yml
CGO_ENABLED ?= 0
LOCAL_RELEASE_ROOT ?= release-local
PROGRAM_TARGET_MATRIX_ALL ?= darwin/amd64,darwin/arm64,linux/amd64,linux/arm64,windows/amd64,windows/arm64

# ARCH detection is intentionally platform-specific. The Windows release path
# must parse without cat/uname/sed or Git Bash being installed.
ifeq ($(OS),Windows_NT)
VERSION :=
ARCH_DETECT := amd64
else
VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
ARCH_DETECT := $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
endif
ARCH ?= $(ARCH_DETECT)

PASS_PROGRAM_TARGETS = $(if $(filter undefined,$(origin PROGRAM_TARGETS)),,PROGRAM_TARGETS=$(PROGRAM_TARGETS))
PASS_PROGRAM_TARGET_MATRIX = $(if $(filter undefined,$(origin PROGRAM_TARGET_MATRIX)),,PROGRAM_TARGET_MATRIX=$(PROGRAM_TARGET_MATRIX))
PASS_PROGRAM_TARGETS_PS = $(if $(filter undefined,$(origin PROGRAM_TARGETS)),,-PROGRAM_TARGETS '$(PROGRAM_TARGETS)')
PASS_PROGRAM_TARGET_MATRIX_PS = $(if $(filter undefined,$(origin PROGRAM_TARGET_MATRIX)),,-PROGRAM_TARGET_MATRIX '$(PROGRAM_TARGET_MATRIX)')
PASS_VERSION_PS = $(if $(strip $(VERSION)),-VERSION '$(VERSION)',)

ifeq ($(OS),Windows_NT)
SHELL := powershell.exe
.SHELLFLAGS := -NoProfile -Command
LOCAL_BINARY := agent-platform.exe
LOCAL_GOOS := windows
else
LOCAL_BINARY := agent-platform
LOCAL_GOOS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
endif

LOCAL_BACKEND_DIR := $(LOCAL_RELEASE_ROOT)/backend
LOCAL_BACKEND_BIN := $(LOCAL_BACKEND_DIR)/$(LOCAL_BINARY)
LOCAL_PLUGINS_DIR := $(LOCAL_RELEASE_ROOT)/plugins
LOCAL_BUILTINS_BIN := build/builtins/$(LOCAL_GOOS)-$(ARCH)/bin

.PHONY: run build-local run-local test test-integration test-release-program-clean docker-build docker-up docker-down release release-program release-program-all clean

ifeq ($(OS),Windows_NT)
run: run-local

build-local:
	@New-Item -ItemType Directory -Path '$(LOCAL_BACKEND_DIR)' -Force | Out-Null; New-Item -ItemType Directory -Path '$(LOCAL_PLUGINS_DIR)' -Force | Out-Null; $$env:CGO_ENABLED = '$(CGO_ENABLED)'; go build -o '$(LOCAL_BACKEND_BIN)' ./cmd/agent-platform; if ($$LASTEXITCODE -ne 0) { exit $$LASTEXITCODE }

run-local: build-local
	@Get-Content .env -ErrorAction SilentlyContinue | ForEach-Object { $$l = $$_.Trim(); if ($$l -and -not $$l.StartsWith('#')) { $$i = $$l.IndexOf('='); if ($$i -gt 0) { [System.Environment]::SetEnvironmentVariable($$l.Substring(0,$$i).Trim(), $$l.Substring($$i+1).Trim(), 'Process') } } }; if ([string]::IsNullOrWhiteSpace($$env:SERVER_PORT)) { $$env:SERVER_PORT = '11949' }; if ([string]::IsNullOrWhiteSpace($$env:AP_BUILTINS_BIN)) { $$env:AP_BUILTINS_BIN = '$(LOCAL_BUILTINS_BIN)' }; if ([string]::IsNullOrWhiteSpace($$env:AP_KBASE_LANCE_ENGINE)) { $$env:AP_KBASE_LANCE_ENGINE = '$(LOCAL_BUILTINS_BIN)/kbase-lance-engine.exe' }; $$env:PATH = '$(LOCAL_BUILTINS_BIN);' + $$env:PATH; & '$(LOCAL_BACKEND_BIN)' --config-dir .
else
run: run-local

build-local:
	mkdir -p "$(LOCAL_BACKEND_DIR)" "$(LOCAL_PLUGINS_DIR)"
	CGO_ENABLED=$(CGO_ENABLED) go build -o "$(LOCAL_BACKEND_BIN)" ./cmd/agent-platform

run-local: build-local
	set -a; [ ! -f .env ] || . ./.env; set +a; SERVER_PORT="$${SERVER_PORT:-11949}" AP_BUILTINS_BIN="$${AP_BUILTINS_BIN:-$(abspath $(LOCAL_BUILTINS_BIN))}" AP_KBASE_LANCE_ENGINE="$${AP_KBASE_LANCE_ENGINE:-$(abspath $(LOCAL_BUILTINS_BIN))/kbase-lance-engine}" PATH="$(abspath $(LOCAL_BUILTINS_BIN)):$$PATH" "$(LOCAL_BACKEND_BIN)" --config-dir .
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

ifeq ($(OS),Windows_NT)
test-release-program-clean:
	powershell -ExecutionPolicy Bypass -File scripts/test-release-program-clean.ps1
else
test-release-program-clean:
	scripts/test-release-program-clean.sh
endif

docker-build:
	VERSION=$(VERSION) docker compose -f $(COMPOSE_FILE) build

docker-up:
	VERSION=$(VERSION) docker compose -f $(COMPOSE_FILE) up -d --build

docker-down:
	VERSION=$(VERSION) docker compose -f $(COMPOSE_FILE) down

release:
	$(MAKE) release-program VERSION=$(VERSION) ARCH=$(ARCH) $(PASS_PROGRAM_TARGETS) $(PASS_PROGRAM_TARGET_MATRIX)

ifeq ($(OS),Windows_NT)
release-program:
	powershell -ExecutionPolicy Bypass -File scripts/release-program.ps1 $(PASS_VERSION_PS) -ARCH '$(ARCH)' $(PASS_PROGRAM_TARGETS_PS) $(PASS_PROGRAM_TARGET_MATRIX_PS)
else
release-program:
	VERSION=$(VERSION) ARCH=$(ARCH) $(PASS_PROGRAM_TARGETS) $(PASS_PROGRAM_TARGET_MATRIX) bash scripts/release-program.sh
endif

release-program-all:
	$(MAKE) release-program PROGRAM_TARGET_MATRIX=$(PROGRAM_TARGET_MATRIX_ALL)

clean:
	rm -rf dist/release
