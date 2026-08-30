GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X github.com/andrii/ephyra/internal/buildinfo.version=$(VERSION)

.PHONY: test lint web build run dev docker

test:
	CGO_ENABLED=0 $(GO) test ./...

lint:
	$(GO) vet ./...
	golangci-lint run

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o ephyra ./cmd/ephyra

run: build
	./ephyra

# two processes: `cd web && npm run dev` (5173, proxies /api) and `go run ./cmd/ephyra` (8080)
dev:
	@echo "terminal 1: cd web && npm run dev"
	@echo "terminal 2: go run ./cmd/ephyra"

docker:
	docker build -t ephyra:$(VERSION) .
