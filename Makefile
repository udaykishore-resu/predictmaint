# predictmaint — predictive maintenance at fleet scale
SHELL := /bin/bash
MODULE := github.com/udaykishore-resu/predictmaint
BIN_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE ?= ghcr.io/udaykishore-resu/predictmaint:$(VERSION)
GOFLAGS_TEST := -race -count=1 -p 1

.PHONY: all build run run-full simulate demo test cover vet lint tidy gen docker helm-lint compose-down clean help

all: build

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build service and simulator binaries into ./bin
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/predictmaint ./cmd/predictmaint
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/simulator ./cmd/simulator

run: build ## Run with in-memory store and simulated CMMS (zero infrastructure)
	STORE_BACKEND=memory LOG_FORMAT=text ./$(BIN_DIR)/predictmaint

run-full: build ## Start Kafka/Postgres/otel-collector via docker compose, then run against them
	docker compose -f deploy/docker-compose.yaml up -d --wait
	STORE_BACKEND=postgres POSTGRES_DSN=postgres://predictmaint:predictmaint@localhost:5432/predictmaint?sslmode=disable \
	KAFKA_ENABLED=true KAFKA_BROKERS=localhost:9092 OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318 \
	LOG_FORMAT=text ./$(BIN_DIR)/predictmaint

compose-down: ## Stop the local full stack
	docker compose -f deploy/docker-compose.yaml down -v

simulate: build ## Stream the 5-pump bearing-degradation scenario into a running service
	./$(BIN_DIR)/simulator -url $${PREDICTMAINT_URL:-http://localhost:8080}

demo: build ## Full scripted demo against a running service (see examples/demo.sh)
	./examples/demo.sh

test: ## Unit tests with the race detector
	go test $(GOFLAGS_TEST) ./...

cover: ## Coverage report for the domain packages
	go test $(GOFLAGS_TEST) -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1

vet: ## go vet
	go vet ./...

lint: ## golangci-lint
	golangci-lint run ./...

tidy: ## go mod tidy
	go mod tidy

gen: ## No code generation in this repo (kept for target parity)
	@echo "nothing to generate"

docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

helm-lint: ## Lint the Helm chart
	helm lint deploy/helm/predictmaint

clean:
	rm -rf $(BIN_DIR) coverage.out
