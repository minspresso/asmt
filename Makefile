.PHONY: build build-arm64 run clean install uninstall dist

BINARY=serverstat
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Build for current platform (requires Go)
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) .

# Cross-compile targets (build on dev machine, deploy anywhere)
build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY) .

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY) .

# Run locally (requires Go)
run:
	go run .

clean:
	rm -f $(BINARY) serverstat-*.tar.gz

# Package a distributable archive (no Go needed on target server)
# Usage: make dist GOARCH=amd64  (or arm64)
dist: build
	tar czf serverstat-$(VERSION)-linux-$(shell go env GOARCH).tar.gz \
		$(BINARY) install.sh uninstall.sh config.yaml README.md

# Install on the local server (uses pre-built binary or builds from source)
install:
	@if [ ! -f "./$(BINARY)" ] && command -v go >/dev/null 2>&1; then \
		echo "Building binary..."; \
		CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) .; \
	fi
	sudo bash install.sh

uninstall:
	sudo bash uninstall.sh
