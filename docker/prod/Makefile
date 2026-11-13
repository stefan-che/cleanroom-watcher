# Scanner production stack management. Run all commands from docker/prod.
#
#   make init      — first run: directories, permissions, build, start
#   make deploy    — update: git pull + rebuild + scanner restart
#   make rollback  — roll back to the previous image
#   make logs      — tail logs of all services

SHELL := /bin/bash
DC    := docker compose

# The image name comes from .env so rollback tags the same image that build produces.
SCANNER_IMAGE ?= $(shell grep -E '^SCANNER_IMAGE=' .env 2>/dev/null | cut -d= -f2)
SCANNER_IMAGE := $(if $(SCANNER_IMAGE),$(SCANNER_IMAGE),cleanroom-watcher-scanner)
NGINX_IMAGE ?= $(shell grep -E '^NGINX_IMAGE=' .env 2>/dev/null | cut -d= -f2)
NGINX_IMAGE := $(if $(NGINX_IMAGE),$(NGINX_IMAGE),cleanroom-watcher-nginx)
MYSQL_IMAGE ?= $(shell grep -E '^MYSQL_IMAGE=' .env 2>/dev/null | cut -d= -f2)
MYSQL_IMAGE := $(if $(MYSQL_IMAGE),$(MYSQL_IMAGE),cleanroom-watcher-mysql)

# Busts the cache of the `apk upgrade` layer in the images (ARG CACHEBUST): otherwise BuildKit
# reuses it from cache and alpine patches never arrive, even with --pull.
export CACHEBUST ?= $(shell date +%F)

.DEFAULT_GOAL := help
.PHONY: help init config check-config build build-nginx up down restart ps logs \
        logs-scanner logs-nginx logs-app health reload-config deploy deploy-fast rollback \
        nginx-reload fix-perms shell db-shell redis-cli backup restore \
        scan prune

help: ## List commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- First run ---------------------------------------------------------------

init: ## First run: check configs, create directories, build and start
	@test -f .env        || { echo "No .env: cp .env.example .env && chmod 600 .env"; exit 1; }
	@test -f config.json || { echo "No config.json: cp config.prod.json.example config.json && chmod 600 config.json"; exit 1; }
	@$(MAKE) check-config
	@$(MAKE) fix-perms
	$(DC) build --pull
	$(DC) up -d
	@echo "Stack is up. Next: make health and front proxy setup (README)"

config: ## Show the resolved compose config (checks variable substitution)
	$(DC) config

check-config: ## Check config.json syntax and that ADMIN_PATH matches .env
	@python3 -c "import json,sys; json.load(open('config.json'))" \
	  && echo "  ok      config.json is valid JSON" \
	  || { echo "  ERROR   config.json does not parse"; exit 1; }
	@set -a; . ./.env; set +a; \
	  cfg_path=$$(python3 -c "import json; print(json.load(open('config.json')).get('admin',{}).get('path',''))"); \
	  if [ "$$cfg_path" = "$$ADMIN_PATH" ]; then \
	    echo "  ok      admin.path matches ADMIN_PATH"; \
	  else \
	    echo "  WARNING admin.path ($$cfg_path) != ADMIN_PATH ($$ADMIN_PATH): nginx will block the admin panel"; \
	  fi

# --- Lifecycle ---------------------------------------------------------------

build: ## Build images (the scanner is built from the repository sources)
	$(DC) build --pull

build-nginx: ## Rebuild nginx with fresh alpine patches and restart it
	CACHEBUST=$$(date +%s) $(DC) build --pull listener-nginx
	$(DC) up -d --force-recreate --no-deps listener-nginx
	@$(DC) exec -T listener-nginx sh -c 'echo "alpine $$(cat /etc/alpine-release)"; nginx -t'

up: ## Start the stack
	$(DC) up -d

down: ## Stop the stack (data in volumes and directories is kept)
	$(DC) down

restart: ## Restart all services
	$(DC) restart

ps: ## Container status
	$(DC) ps

health: ## Container state + /health response from the scanner
	@$(DC) ps --format 'table {{.Name}}\t{{.Status}}'
	@echo
	@$(DC) exec -T listener-scanner wget -qO- http://127.0.0.1:8080/health \
	  | python3 -m json.tool 2>/dev/null || echo "scanner is not responding on /health"

# --- Logs --------------------------------------------------------------------

logs: ## Tail logs of all services
	$(DC) logs -f --tail=200

logs-scanner: ## Tail scanner logs (container stdout)
	$(DC) logs -f --tail=200 listener-scanner

logs-nginx: ## Tail the nginx access log
	tail -f logs/nginx/access.log

logs-app: ## Tail the application file log (logs/scanner/YYYY-MM-DD.log)
	@tail -f "logs/scanner/$$(date -u +%Y-%m-%d).log"

# --- Deploy ------------------------------------------------------------------

reload-config: ## Apply config.json edits: check + scanner restart (no build)
	@# config.json is mounted read-only from the host and is not baked into the image, so
	@# no rebuild is needed; restarting the process is enough.
	@# .env edits are not picked up this way: they need docker compose up -d,
	@# and changing admin.path also requires recreating nginx (see README).
	@$(MAKE) check-config
	$(DC) restart listener-scanner
	@echo "waiting for the scanner to become healthy"
	@cid=$$($(DC) ps -q listener-scanner); \
	  test -n "$$cid" || { echo "scanner container is not running: run make up first"; exit 1; }; \
	  for i in $$(seq 1 30); do \
	    status=$$(docker inspect -f '{{.State.Health.Status}}' "$$cid" 2>/dev/null || echo unknown); \
	    case "$$status" in \
	      healthy)   break ;; \
	      unhealthy) echo "scanner is unhealthy. Logs: make logs-scanner"; exit 1 ;; \
	    esac; \
	    if [ "$$i" -eq 30 ]; then \
	      echo "scanner did not come up within 2.5 minutes. Logs: make logs-scanner"; exit 1; \
	    fi; \
	    sleep 5; \
	  done
	@$(MAKE) health

deploy: ## Update production: git pull, image rebuild, scanner restart
	@./deploy.sh

deploy-fast: ## Same, but without git pull (code is already in place)
	@./deploy.sh --no-pull

rollback: ## Roll back to the previous scanner image (tag :prev)
	@docker image inspect $(SCANNER_IMAGE):prev >/dev/null 2>&1 \
	  || { echo "No image $(SCANNER_IMAGE):prev: nothing to roll back to"; exit 1; }
	docker image tag $(SCANNER_IMAGE):prev $(SCANNER_IMAGE):prod
	$(DC) up -d --force-recreate --no-deps listener-scanner
	@$(MAKE) health

nginx-reload: ## Reload the nginx config (after editing access/*.inc)
	$(DC) exec -T listener-nginx nginx -t && $(DC) exec -T listener-nginx nginx -s reload

# --- Access ------------------------------------------------------------------

shell: ## Shell in the scanner container (busybox, diagnostics only)
	$(DC) exec listener-scanner sh

db-shell: ## MySQL client
	$(DC) exec listener-mysql sh -c 'mysql -u root -p"$$MYSQL_ROOT_PASSWORD" $$MYSQL_DATABASE'

redis-cli: ## Redis client
	$(DC) exec listener-redis sh -c 'redis-cli -a "$$REDIS_PASSWORD"'

fix-perms: ## Create working directories and hand them to the PUID/PGID user
	@# Docker creates missing mount points as root; then
	@# the application cannot write logs/YYYY-MM-DD.log and the admin panel shows nothing.
	@set -a; . ./.env; set +a; \
	  mkdir -p logs/scanner logs/nginx backups "$${MYSQL_DATA_PATH:-./data/mysql}" "$${REDIS_DATA_PATH:-./data/redis}"; \
	  echo "chown $${PUID:-1000}:$${PGID:-1000} logs/scanner"; \
	  chown -R "$${PUID:-1000}:$${PGID:-1000}" logs/scanner 2>/dev/null \
	    || sudo chown -R "$${PUID:-1000}:$${PGID:-1000}" logs/scanner; \
	  mysql_dir="$${MYSQL_DATA_PATH:-./data/mysql}"; \
	  if [ "$$(stat -c %u "$$mysql_dir" 2>/dev/null)" != "999" ]; then \
	    echo "chown 999:999 $$mysql_dir (MySQL runs as mysql, not root)"; \
	    sudo chown -R 999:999 "$$mysql_dir"; \
	  fi

# --- Maintenance -------------------------------------------------------------

backup: ## Dump the scanner DB to ./backups
	@mkdir -p backups
	$(DC) exec -T listener-mysql sh -c 'exec mysqldump -u root -p"$$MYSQL_ROOT_PASSWORD" \
		--single-transaction --quick --routines --triggers --events \
		"$$MYSQL_DATABASE"' | gzip > backups/dump-$$(date +%Y%m%d-%H%M%S).sql.gz
	@echo "Dump ready: $$(ls -t backups/*.sql.gz | head -1)"

restore: ## Restore the DB from a dump: make restore FILE=backups/dump-....sql.gz
	@test -n "$(FILE)" || { echo "Specify the file: make restore FILE=backups/dump-....sql.gz"; exit 1; }
	@test -f "$(FILE)" || { echo "File $(FILE) not found"; exit 1; }
	@echo "WARNING: current DB data will be overwritten from $(FILE)."
	@read -p "Continue? [y/N] " ans; [ "$$ans" = "y" ] || exit 1; \
	  $(DC) stop listener-scanner; \
	  gunzip -c "$(FILE)" | $(DC) exec -T listener-mysql sh -c \
	    'exec mysql -u root -p"$$MYSQL_ROOT_PASSWORD" "$$MYSQL_DATABASE"'; \
	  $(DC) start listener-scanner
	@echo "Restored. Check: make health"

# --- Security ----------------------------------------------------------------

scan: ## Scan images for vulnerabilities (trivy, needs internet)
# The vulnerability DB (~120 MB) lives in the trivy-cache volume: downloaded once
# per run, not again for every image.
	@echo "--- $(SCANNER_IMAGE):prod"
	@docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v trivy-cache:/root/.cache \
		aquasec/trivy image --ignore-unfixed --scanners vuln $(SCANNER_IMAGE):prod
	@echo "--- $(NGINX_IMAGE):prod"
	@docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v trivy-cache:/root/.cache \
		aquasec/trivy image --ignore-unfixed --scanners vuln $(NGINX_IMAGE):prod
	@echo "--- $(MYSQL_IMAGE):prod"
	@docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v trivy-cache:/root/.cache \
		aquasec/trivy image --ignore-unfixed --scanners vuln $(MYSQL_IMAGE):prod
	@echo "--- go.mod"
	@docker run --rm -v "$$(cd ../.. && pwd)":/scan:ro -v trivy-cache:/root/.cache \
		aquasec/trivy fs --scanners vuln /scan/go.mod

prune: ## Remove old unused build images (leaves the :prev tag alone)
	docker image prune -f --filter "dangling=true"
