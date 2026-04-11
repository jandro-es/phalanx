# =============================================================================
# Phalanx — Makefile
# =============================================================================

.PHONY: build run test lint migrate seed docker-build docker-up docker-down clean

# Build
build:
	go build -o bin/phalanx-server ./cmd/server
	go build -o bin/phalanx ./cmd/cli

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/phalanx-server-linux ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/phalanx-linux ./cmd/cli

# Run
run:
	go run ./cmd/server

dev:
	go run ./cmd/server

# Test
test:
	go test ./... -v -race -count=1

test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# Lint
lint:
	golangci-lint run ./...

vet:
	go vet ./...

# Database
migrate:
	psql $(DATABASE_URL) -f migrations/001_initial.sql

# Seed built-in skills (requires server running)
seed:
	@for f in skills/*.yaml; do \
		echo "Registering $$f..."; \
		./bin/phalanx skill register "$$f" --server http://localhost:3100; \
	done

# Docker
docker-build:
	docker build -f deploy/Dockerfile -t phalanx .

docker-up:
	docker compose -f deploy/docker-compose.yml up -d

docker-down:
	docker compose -f deploy/docker-compose.yml down

# Helm
helm-install:
	helm install phalanx deploy/helm/phalanx

helm-upgrade:
	helm upgrade phalanx deploy/helm/phalanx

# Clean
clean:
	rm -rf bin/ coverage.out coverage.html
