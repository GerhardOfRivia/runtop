OUTPUT ?= runtop
SEMVER ?= 1.0.0
VERSION := $(SEMVER)-dev
LDFLAGS = -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build test clean

all: build

build-linux-amd64:
	@echo "Building runbook for Linux AMD64 with version $(VERSION)..."
	go build $(LDFLAGS) -o $(OUTPUT)-linux-amd64 ./src

build-linux-arm64:
	@echo "Building runbook for Linux ARM64 with version $(VERSION)..."
	go build $(LDFLAGS) -o $(OUTPUT)-linux-arm64 ./src

build-darwin-amd64:
	@echo "Building runbook for Darwin AMD64 with version $(VERSION)..."
	go build $(LDFLAGS) -o $(OUTPUT)-darwin-amd64 ./src

build-darwin-arm64:
	@echo "Building runbook for Darwin ARM64 with version $(VERSION)..."
	go build $(LDFLAGS) -o $(OUTPUT)-darwin-arm64 ./src

build:
	go build $(LDFLAGS) -o $(OUTPUT) ./src

test:
	@echo "Running tests..."
	go test -v ./...

clean:
	@echo "Cleaning up..."
	rm -f $(OUTPUT)
