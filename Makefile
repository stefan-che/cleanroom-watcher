COMPOSE = docker compose
SHARED_NETWORK ?= processing-net

.PHONY: up down restart build rebuild logs logs-scanner logs-db ps \
        db-reset db-shell run network config

## ── Docker ──────────────────────────────────────────────────────────────────

## Shared docker network with tron-cleanroom (idempotent)
network:
	@docker network inspect $(SHARED_NETWORK) >/dev/null 2>&1 \
		|| docker network create $(SHARED_NETWORK)

## Create config.json from config.docker.json.example (does not overwrite)
config:
	@test -f config.json && echo "config.json already exists" \
		|| { cp config.docker.json.example config.json && echo "created config.json - edit it (README: Quick start)"; }

## Start the whole stack (without rebuilding the image)
up: network
	@test -f config.json || { echo "config.json not found: run make config and edit it"; exit 1; }
	$(COMPOSE) up -d

## Stop all containers
down:
	$(COMPOSE) down

## Restart without rebuilding (picks up config.json changes)
restart:
	$(COMPOSE) up -d --force-recreate

## Rebuild the scanner image and restart it
build:
	$(COMPOSE) build --no-cache scanner
	$(COMPOSE) up -d --force-recreate --no-deps scanner

## Rebuild all images from scratch and start the stack
rebuild:
	$(COMPOSE) build --no-cache
	$(COMPOSE) up -d --force-recreate

## ── Logs ────────────────────────────────────────────────────────────────────

logs:
	$(COMPOSE) logs -f

logs-scanner:
	$(COMPOSE) logs -f scanner

logs-db:
	$(COMPOSE) logs -f mysql

## Container status
ps:
	$(COMPOSE) ps

## ── Database ────────────────────────────────────────────────────────────────

## Reset the DB: remove the data volume and recreate the mysql container.
## Use when migrations need to be applied from scratch.
db-reset:
	$(COMPOSE) down -v
	$(COMPOSE) up -d mysql redis
	@echo "Waiting for MySQL to be ready..."
	@until $(COMPOSE) exec mysql sh -c 'mysqladmin ping -h 127.0.0.1 -u tron -p"$$MYSQL_PASSWORD" --silent' 2>/dev/null; do sleep 1; done
	@echo "MySQL ready. Starting scanner (runs migrations on start)..."
	$(COMPOSE) up -d scanner

## Open a MySQL shell
db-shell:
	$(COMPOSE) exec mysql sh -c 'mysql -u tron -p"$$MYSQL_PASSWORD" tron_scanner'

## ── Local development ───────────────────────────────────────────────────────

## Build the binary locally
bin:
	go build -o scanner ./cmd/scanner

## Run locally (requires running mysql + redis)
run:
	go run ./cmd/scanner

## ── Server deploy (bare-metal, pm2 + nginx) ─────────────────────────

## Deploy: build + upload + restart (server settings live in deploy/prod/deploy.sh)
deploy:
	./deploy/prod/deploy.sh prod

## Deploy without rebuilding (a built binary already exists)
deploy-fast:
	./deploy/prod/deploy.sh prod --skip-build
