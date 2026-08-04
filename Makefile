.PHONY: build check-no-reference-deps test test-race test-upstream test-container

build:
	CGO_ENABLED=0 go build -trimpath -o build/wg-quic ./cmd/wg-quic

test:
	./scripts/check-no-reference-deps.sh
	go test ./...

check-no-reference-deps:
	./scripts/check-no-reference-deps.sh

test-race:
	go test -race ./...

test-upstream:
	./scripts/test-upstream-wireguard.sh

test-container:
	./tests/container/test.sh
