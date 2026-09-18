BIN ?= $(HOME)/go/bin/coord

.PHONY: build test install
build:
	go build -o coord .
test:
	go test ./...
install:
	go build -o $(BIN) .
