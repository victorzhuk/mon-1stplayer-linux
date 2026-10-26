SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

export CGO_ENABLED := 1
export GOMAXPROCS := 2

BIN := bin

.PHONY: all build test test-race acceptance-build lint lint-acceptance vuln clean

all: build test

build:
	mkdir -p $(BIN)
	timeout --kill-after=5s 120s go build -mod=readonly -p 2 -o bin/mon-1stplayer .

test:
	timeout --kill-after=5s 150s go test -mod=readonly -p 2 -parallel 1 -timeout 2m ./...

test-race:
	timeout --kill-after=5s 300s go test -mod=readonly -p 2 -parallel 1 -race -timeout 2m ./...

acceptance-build:
	mkdir -p $(BIN)
	timeout --kill-after=5s 120s go test -mod=readonly -p 2 -tags acceptance -c -o bin/acceptance.test .

lint:
	timeout --kill-after=5s 330s golangci-lint run ./...

lint-acceptance:
	timeout --kill-after=5s 330s golangci-lint run --build-tags acceptance ./...

vuln:
	timeout --kill-after=5s 180s go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

clean:
	rm -rf $(BIN)
