.PHONY: build vet test test-extension fmt lint check install

build:
	go build ./...

vet:
	go vet ./...

test:
	go test -race ./...

test-extension:
	node --test scripts/test-canopy-status.ts

fmt:
	gofmt -l .

lint:
	golangci-lint run ./...

check: fmt vet build test test-extension

install:
	go build -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o /tmp/canopy-build ./cmd/canopy
	install -m 0755 /tmp/canopy-build ~/.local/bin/canopy
