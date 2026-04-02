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
	rm -rf /tmp/serverstat-*/

# Package a distributable archive (no Go needed on target server)
# Usage: make dist GOARCH=amd64  (or arm64)
# Produces: serverstat-VERSION-linux-ARCH.tar.gz
#   Extracts to: serverstat-VERSION-linux-ARCH/
#     ├── serverstat
#     ├── scripts/install.sh
#     ├── scripts/uninstall.sh
#     ├── config.yaml
#     └── README.md
dist: build
	$(eval DIST_NAME := serverstat-$(VERSION)-linux-$(shell go env GOARCH))
	mkdir -p /tmp/$(DIST_NAME)/scripts
	cp $(BINARY) /tmp/$(DIST_NAME)/
	cp scripts/install.sh scripts/uninstall.sh /tmp/$(DIST_NAME)/scripts/
	cp config.yaml README.md /tmp/$(DIST_NAME)/
	tar -czf $(DIST_NAME).tar.gz -C /tmp $(DIST_NAME)
	rm -rf /tmp/$(DIST_NAME)
	@echo "Created: $(DIST_NAME).tar.gz"

# Install on the local server (uses pre-built binary or builds from source)
install:
	@if [ ! -f "./$(BINARY)" ] && command -v go >/dev/null 2>&1; then \
		echo "Building binary..."; \
		CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) .; \
	fi
	sudo bash scripts/install.sh

uninstall:
	sudo bash scripts/uninstall.sh
