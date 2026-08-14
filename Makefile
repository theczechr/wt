.PHONY: build install test

build:
	go build -o ./wt ./cmd/wt

install:
	go build -o $(HOME)/.local/bin/wt ./cmd/wt

test:
	go test ./...
