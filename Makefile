.PHONY: build run clean test deps help docker-build

# Detect OS and arch
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

ifeq ($(UNAME_S),Darwin)
    OS := macos
    CGO_CFLAGS := -I/opt/homebrew/include
    CGO_LDFLAGS := -L/opt/homebrew/lib
else
    OS := linux
    CGO_CFLAGS :=
    CGO_LDFLAGS :=
endif

ifeq ($(UNAME_M),arm64)
    ARCH := arm64
else ifeq ($(UNAME_M),aarch64)
    ARCH := arm64
else
    ARCH := amd64
endif

BINARY := ash-$(OS)-$(ARCH)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

deps: ## Install Go dependencies
	go mod download
	go mod tidy

build: ## Build the ash binary (builds package)
	CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" go build -o $(BINARY) ./cmd/ash

run: build ## Build and run the ash single-file binary
	./$(BINARY)

clean: ## Remove built binaries and generated files
	rm -f ash-*-*
	rm -rf ./data/*

test: ## Run tests
	go test ./...
	cd test && go test -v

docker-build: ## Build using Docker for cross-compilation to Ubuntu
	docker build --platform linux/amd64 -t ash .
	docker run --rm -v $(PWD):/host ash cp /usr/local/bin/ash /host/ash-linux-amd64

# Pull configuration
RSYNC_OPTS ?= -avzhP --delete
PULL_SRC ?= ark:ash/data/

pull: ## Pull the remote 'data' directory into local ./data/ (overwrites). Set PULL_SRC to change source.
	@mkdir -p data
	rsync $(RSYNC_OPTS) $(PULL_SRC) ./data/

.DEFAULT_GOAL := run

ci: docker-build
	rsync -avzP ash-linux-amd64 'ark:ash/ash-linux-amd64'
	rsync -avzP *.json 'ark:ash' 
	ssh ark sudo systemctl restart ash.service
