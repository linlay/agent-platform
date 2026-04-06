COMPOSE_FILE ?= compose.yml
CGO_ENABLED ?= 0

.PHONY: run test test-integration docker-build docker-up docker-down

run:
	set -a; [ ! -f .env ] || . ./.env; set +a; SERVER_PORT="$${HOST_PORT:-$${SERVER_PORT:-8080}}" CGO_ENABLED=$(CGO_ENABLED) go run ./cmd/agent-platform-runner

test:
	@for pkg in $$(go list ./...); do \
		attempt=1; \
		while true; do \
			cache_dir=$$(mktemp -d); \
			if GOCACHE=$$cache_dir CGO_ENABLED=$(CGO_ENABLED) go test $$pkg; then \
				rm -rf $$cache_dir; \
				break; \
			fi; \
			status=$$?; \
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
