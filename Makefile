.PHONY: run build migrate test docker-up docker-down

# ── Local development ──────────────────────────────────────────
run:
	go run ./cmd/server/...

run-backoffice:
	go run ./cmd/backoffice/...

build:
	go build -o bin/server     ./cmd/server/...
	go build -o bin/backoffice ./cmd/backoffice/...

# ── Database migrations (requires psql in PATH) ────────────────
migrate:
	psql "$$DATABASE_URL" -f migrations/001_init.sql
	psql "$$DATABASE_URL" -f migrations/002_indexes.sql
	psql "$$DATABASE_URL" -f migrations/003_mm.sql

migrate-docker:
	docker exec -i evetabi-db psql -U $${DB_USER:-postgres} -d $${DB_NAME:-evetabi_prediction} < migrations/001_init.sql
	docker exec -i evetabi-db psql -U $${DB_USER:-postgres} -d $${DB_NAME:-evetabi_prediction} < migrations/002_indexes.sql
	docker exec -i evetabi-db psql -U $${DB_USER:-postgres} -d $${DB_NAME:-evetabi_prediction} < migrations/003_mm.sql

# ── Testing ────────────────────────────────────────────────────
test:
	go test ./... -v -race -count=1

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# ── Docker ─────────────────────────────────────────────────────
docker-up:
	docker-compose up -d --build

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

# ── Helpers ────────────────────────────────────────────────────
tidy:
	go mod tidy

clean:
	rm -rf bin/
