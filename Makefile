.PHONY: run build test test-race lint tidy docker dev clean

BINARY := whatsapp-mcp
PKG    := ./...

run:
	go run ./cmd/server

dev:
	@command -v air >/dev/null 2>&1 || (echo "install air: go install github.com/air-verse/air@latest" && exit 1)
	air -c .air.server.toml

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) ./cmd/server

test:
	go test -count=1 $(PKG)

test-race:
	go test -count=1 -race $(PKG)

lint:
	go vet $(PKG)
	gofmt -l . | tee /dev/stderr | (! grep -q .)

tidy:
	go mod tidy

docker:
	docker build -t $(BINARY):latest .

clean:
	rm -rf bin tmp coverage.txt
