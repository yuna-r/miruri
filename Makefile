BINARY := bin/miruri
VERSION := 0.1.0-alpha.7
COMMIT := $(shell c=$$(git rev-parse --short HEAD 2>/dev/null || echo dev); if ! git diff --quiet --ignore-submodules HEAD 2>/dev/null || test -n "$$(git ls-files --others --exclude-standard 2>/dev/null)"; then c="$$c-dirty"; fi; printf '%s' "$$c")
LDFLAGS := -s -w \
	-X github.com/yuna-r/miruri/internal/cli.Version=$(VERSION) \
	-X github.com/yuna-r/miruri/internal/cli.Commit=$(COMMIT) \
	-X github.com/yuna-r/miruri/internal/cli.Date=$$(date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: all build test test-race vet fmt check smoke codex-smoke tools clean

all: check build

build:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/miruri

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check:
	test -z "$$(gofmt -l .)"
	go test ./...
	go vet ./...

smoke: build
	./scripts/smoke.sh ./$(BINARY)

codex-smoke: build
	./scripts/codex-smoke.sh ./$(BINARY)

tools: build
	mkdir -p dist-tools
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist-tools/miruri-darwin-arm64 ./cmd/miruri
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist-tools/miruri-linux-amd64 ./cmd/miruri

clean:
	rm -rf bin dist dist-tools .test-dist
