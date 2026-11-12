.PHONY: build test vet

build:
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o bin/salad-preflight ./cmd/salad-preflight

test:
	go test -race ./...

vet:
	go vet ./...
