.PHONY: build build-windows check-no-reference-deps check-third-party test test-race test-wireguard test-transport test-container

build:
	mkdir -p build
	CGO_ENABLED=0 go build -trimpath -o build/wg-quic ./cmd/wg-quic
	CGO_ENABLED=0 go build -trimpath -o build/wg-quic-quick ./cmd/wg-quic-quick

build-windows:
	./scripts/package-windows.sh amd64
	./scripts/package-windows.sh arm64

test:
	./scripts/check-no-reference-deps.sh
	go test ./...

check-no-reference-deps:
	./scripts/check-no-reference-deps.sh

check-third-party:
	cd third_party/wintun && sha256sum -c SHA256SUMS

test-race:
	go test -race ./...

test-wireguard:
	go test -count=1 ./third_party/wireguard-go/...

test-transport:
	go test -count=1 -run '^TestWireGuard' ./internal/bind

test-container:
	./tests/container/test.sh
