WITH_MODELS ?= 1

.PHONY: up down credentials logs status models verify test pilot-up pilot-down backup restore-check gmail-connect
up:
	./scripts/dev-setup.sh
	docker compose up --build -d --wait --remove-orphans db audio voicefm api frontend
	@if [ "$(WITH_MODELS)" = "1" ]; then $(MAKE) models && $(MAKE) verify; fi
credentials:
	./scripts/local-credentials.sh
down:
	docker compose down
logs:
	docker compose logs --tail=100 -f
status:
	./scripts/stand-status.py
models:
	./scripts/dev-setup.sh
	docker compose run --build --rm models
	docker compose restart audio
	./scripts/wait-models.sh
verify:
	./scripts/stand-status.py --strict
test:
	./scripts/test-all.sh
pilot-up:
	./scripts/pilot-up.sh
pilot-down:
	docker compose -f compose.yaml -f compose.pilot.yaml down
backup:
	./scripts/backup.sh $(BACKUP)
restore-check:
	./scripts/restore-check.sh $(BACKUP)
gmail-connect:
	./scripts/gmail-connect.sh
