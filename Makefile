COMPOSE_FILE ?= compose.yml

.PHONY: run test docker-build docker-up docker-down

run:
	set -a; [ ! -f .env ] || . ./.env; set +a; SERVER_PORT="$${HOST_PORT:-$${SERVER_PORT:-8080}}" go run ./cmd/agent-platform-runner

test:
	go test ./...

docker-build:
	docker compose -f $(COMPOSE_FILE) build

docker-up:
	docker compose -f $(COMPOSE_FILE) up -d --build

docker-down:
	docker compose -f $(COMPOSE_FILE) down
