VERSION ?= $(shell test -f VERSION && sed -n '1p' VERSION || echo 0.1.0-dev)
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build build-windows check-no-reference-deps check-third-party release-artifacts test test-race test-wireguard test-quic test-transport test-container

build:
	mkdir -p build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/wg-quic ./cmd/wg-quic
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/wg-quic-quick ./cmd/wg-quic-quick

build-windows:
	WG_QUIC_VERSION="$(VERSION)" ./scripts/package-windows.sh amd64
	WG_QUIC_VERSION="$(VERSION)" ./scripts/package-windows.sh arm64

release-artifacts:
	./scripts/package-release.sh linux amd64 "$(VERSION)"
	./scripts/package-release.sh linux arm64 "$(VERSION)"
	./scripts/package-release.sh freebsd amd64 "$(VERSION)"
	./scripts/package-release.sh freebsd arm64 "$(VERSION)"
	./scripts/package-release.sh windows amd64 "$(VERSION)"
	./scripts/package-release.sh windows arm64 "$(VERSION)"

test:
	./scripts/check-no-reference-deps.sh
	go test ./...

check-no-reference-deps:
	./scripts/check-no-reference-deps.sh

check-third-party:
	cd third_party/wintun && sha256sum -c SHA256SUMS
	cd third_party/quic-go && go test ./...

test-race:
	go test -race ./...

test-wireguard:
	go test -count=1 ./third_party/wireguard-go/...

test-quic:
	cd third_party/quic-go && go test -count=1 ./...

test-transport:
	go test -count=1 -run '^TestWireGuard' ./internal/bind

test-container:
	./tests/container/test.sh
