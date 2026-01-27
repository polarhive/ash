.PHONY: build run clean test deps help docker-build

# Detect OS for CGO flags
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
    # macOS with Homebrew
    CGO_CFLAGS := -I/opt/homebrew/include
    CGO_LDFLAGS := -L/opt/homebrew/lib
else
    # Linux - assume system paths work
    CGO_CFLAGS := 
    CGO_LDFLAGS := 
endif

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

deps: ## Install Go dependencies
	go mod download
	go mod tidy

build: ## Build the ash single-file binary
	CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" go build -o ash ./ash.go

run: build ## Build and run the ash single-file binary
	./ash

clean: ## Remove built binaries and generated files
	rm -f ash
	rm -rf ./data/*

test: ## Run tests
	go test -v ./...

docker-build: ## Build using Docker for cross-compilation to Ubuntu
	docker build --platform linux/amd64 -t ash .
	docker run --rm -v $(PWD):/host ash cp /usr/local/bin/ash /host/ash-linux-amd64
	scp ash-linux-amd64 ark:ash/
.DEFAULT_GOAL := run
