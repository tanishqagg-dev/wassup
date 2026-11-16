VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/tanishqagg-dev/wassup/internal/engine.Version=$(VERSION)
PREFIX  ?= $(HOME)/.local

.PHONY: build test vet install clean release-snapshot

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/wassup ./cmd/wassup

test:
	go test ./...

vet:
	go vet ./...

install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/wassup $(PREFIX)/bin/wassup
	@echo "installed $(PREFIX)/bin/wassup"
	@if $(PREFIX)/bin/wassup daemon status --json 2>/dev/null | grep -q '"running":true'; then $(PREFIX)/bin/wassup daemon restart >/dev/null && echo "restarted the running daemon"; fi

clean:
	rm -rf bin dist

release-snapshot:
	goreleaser release --snapshot --clean
