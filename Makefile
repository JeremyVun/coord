BIN ?= $(HOME)/go/bin/coord

.PHONY: build test check install
build:
	go build -trimpath -o coord .
test:
	go test ./...
check:
	test -z "$$(gofmt -l *.go)"
	go vet ./...
	go test -race ./...
install:
	mkdir -p "$$(dirname "$(BIN)")"
	go build -trimpath -o "$(BIN)" .
