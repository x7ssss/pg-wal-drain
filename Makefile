# Makefile for pg-wal-drain

MODULE=github.com/x7ssss/pg-wal-drain
BINARY=pg-wal-drain
LDFLAGS=-s -w

.PHONY: all test build cross-compile clean

all: test build

test:
	go test -v ./...

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pg-wal-drain

cross-compile:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 ./cmd/pg-wal-drain
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 ./cmd/pg-wal-drain
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64 ./cmd/pg-wal-drain
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 ./cmd/pg-wal-drain
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe ./cmd/pg-wal-drain

clean:
	rm -rf dist $(BINARY) $(BINARY).exe
