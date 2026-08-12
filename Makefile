OUTPUT ?= runtop
SEMVER ?= 1.1.1
VERSION := $(SEMVER)-dev
LDFLAGS = -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build test clean

all: build

build-linux-amd64:
	go build $(LDFLAGS) -o $(OUTPUT)-linux-amd64 ./src

build-linux-arm64:
	go build $(LDFLAGS) -o $(OUTPUT)-linux-arm64 ./src

build-darwin-amd64:
	go build $(LDFLAGS) -o $(OUTPUT)-darwin-amd64 ./src

build-darwin-arm64:
	go build $(LDFLAGS) -o $(OUTPUT)-darwin-arm64 ./src

build:
	go build $(LDFLAGS) -o $(OUTPUT) ./src

test:
	go test -v ./...

clean:
	rm -f $(OUTPUT)
