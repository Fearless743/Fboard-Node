VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
REPO_BASE ?= https://github.com/Fearless743/Fboard-Node/releases
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.buildTime=$(BUILD_TIME) \
	-X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
	-X 'main.downloadBase=$(REPO_BASE)' \
	-X github.com/fearless743/fboard-node/internal/buildinfo.Version=$(VERSION) \
	-X github.com/fearless743/fboard-node/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build clean test docker install \
	build-linux build-linux-arm64 build-freebsd build-freebsd-arm64 build-all \
	pack-linux pack-linux-arm64 pack-freebsd pack-freebsd-arm64 pack-all

# Build for current platform
build:
	go build -ldflags "$(LDFLAGS)" -tags "with_quic with_utls with_wireguard with_clash_api" -o fboard-node ./cmd/fboard-node
	go build -ldflags "$(LDFLAGS)" -o fbctl ./cmd/fbctl

# Build for Linux amd64
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -tags "with_quic with_utls with_wireguard with_acme with_clash_api" -o fboard-node-linux-amd64 ./cmd/fboard-node
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o fbctl-linux-amd64 ./cmd/fbctl

# Build for Linux arm64
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -tags "with_quic with_utls with_wireguard with_acme with_clash_api" -o fboard-node-linux-arm64 ./cmd/fboard-node
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o fbctl-linux-arm64 ./cmd/fbctl

# Build for FreeBSD amd64
build-freebsd:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -tags "with_quic with_utls with_wireguard with_acme with_clash_api" -o fboard-node-freebsd-amd64 ./cmd/fboard-node
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o fbctl-freebsd-amd64 ./cmd/fbctl

# Build for FreeBSD arm64
build-freebsd-arm64:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -tags "with_quic with_utls with_wireguard with_acme with_clash_api" -o fboard-node-freebsd-arm64 ./cmd/fboard-node
	CGO_ENABLED=0 GOOS=freebsd GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o fbctl-freebsd-arm64 ./cmd/fbctl

# Pack release archives: each archive contains fboard-node + fbctl
define pack_archive
	@set -e; \
	stage=$$(mktemp -d); \
	install -m 755 fboard-node-$(1)-$(2) $$stage/fboard-node; \
	install -m 755 fbctl-$(1)-$(2) $$stage/fbctl; \
	tar -C $$stage -czf fboard-node-$(1)-$(2).tar.gz fboard-node fbctl; \
	rm -rf $$stage; \
	echo "packed fboard-node-$(1)-$(2).tar.gz"
endef

pack-linux: build-linux
	$(call pack_archive,linux,amd64)

pack-linux-arm64: build-linux-arm64
	$(call pack_archive,linux,arm64)

pack-freebsd: build-freebsd
	$(call pack_archive,freebsd,amd64)

pack-freebsd-arm64: build-freebsd-arm64
	$(call pack_archive,freebsd,arm64)

# Build all platforms
build-all: build-linux build-linux-arm64 build-freebsd build-freebsd-arm64

# Build and pack all release archives
pack-all: pack-linux pack-linux-arm64 pack-freebsd pack-freebsd-arm64

# Run tests
test:
	go test -v -race -count=1 ./internal/...

# Clean build artifacts
clean:
	rm -f fboard-node fbctl \
		fboard-node-linux-* fbctl-linux-* \
		fboard-node-freebsd-* fbctl-freebsd-* \
		fboard-node-*.tar.gz

# Install to system (single node, legacy compat)
install: build
	sudo cp fboard-node /usr/local/bin/
	sudo cp fbctl /usr/local/bin/
	sudo mkdir -p /etc/fboard-node
	@if [ ! -f /etc/fboard-node/config.yml ]; then \
		sudo cp config.yml.example /etc/fboard-node/config.yml; \
		echo "Config copied to /etc/fboard-node/config.yml - please edit it"; \
	fi
