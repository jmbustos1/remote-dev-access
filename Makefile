VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt termux clean

build: ## agente y cli para esta máquina → bin/
	go build -ldflags '$(LDFLAGS)' -o bin/rda ./cli
	go build -ldflags '$(LDFLAGS)' -o bin/rda-agent ./agent

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

termux: ## cli para el celular (Android arm64, Go puro) → bin/rda-android-arm64
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o bin/rda-android-arm64 ./cli

clean:
	rm -rf bin/
