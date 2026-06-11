.PHONY: build test run fmt

build:
	go build -o fake-komga-115 ./cmd/server

test:
	go test ./...

run:
	go run ./cmd/server

fmt:
	gofmt -w cmd internal
