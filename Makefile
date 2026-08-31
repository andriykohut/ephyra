GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X github.com/andriykohut/ephyra/internal/buildinfo.version=$(VERSION)

.PHONY: test lint web build run dev docker dist

DIST_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

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

# two processes: `cd web && npm run dev` (5173, proxies /api) and `go run ./cmd/ephyra` (8097)
dev:
	@echo "terminal 1: cd web && npm run dev"
	@echo "terminal 2: go run ./cmd/ephyra"

docker:
	docker build -t ephyra:$(VERSION) .

# cross-compiled release tarballs into ./dist — the same set release.yml ships.
# Pass VERSION=vX.Y.Z to stamp it; defaults to "dev".
dist: web
	rm -rf dist && mkdir -p dist
	@for p in $(DIST_PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; d=ephyra_$(VERSION)_$${os}_$${arch}; \
	  echo "-> $$d"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$d/ephyra ./cmd/ephyra || exit 1; \
	  cp LICENSE NOTICES.md README.md dist/$$d/; \
	  tar -C dist -czf dist/$$d.tar.gz $$d && rm -rf dist/$$d; \
	done
	cd dist && sha256sum ephyra_*.tar.gz > SHA256SUMS
	@ls -1 dist
