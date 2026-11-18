.PHONY: reader build build-go test run fmt

reader:
	bash scripts/build-reader.sh

build: reader
	CGO_ENABLED=0 go build -o fake-komga-115 ./cmd/server

build-go:
	CGO_ENABLED=0 go build -o fake-komga-115 ./cmd/server

test:
	go test ./...

run:
	go run ./cmd/server

fmt:
	gofmt -w cmd internal
