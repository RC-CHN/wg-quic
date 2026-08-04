.PHONY: build check-no-reference-deps test test-race test-wireguard test-transport test-container

build:
	mkdir -p build
	CGO_ENABLED=0 go build -trimpath -o build/wg-quic ./cmd/wg-quic
	CGO_ENABLED=0 go build -trimpath -o build/wg-quic-quick ./cmd/wg-quic-quick

test:
	./scripts/check-no-reference-deps.sh
	go test ./...

check-no-reference-deps:
	./scripts/check-no-reference-deps.sh

test-race:
	go test -race ./...

test-wireguard:
	go test -count=1 ./third_party/wireguard-go/...

test-transport:
	go test -count=1 -run '^TestWireGuard' ./internal/bind

test-container:
	./tests/container/test.sh
