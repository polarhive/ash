# Use official Golang image (Ubuntu-based) with Olm dev libraries for static linking
FROM golang:latest AS builder

# Install build tools and Olm development libraries
RUN apt-get update && apt-get install -y \
    build-essential \
    libolm-dev

# Set Go environment variables for CGO
ENV CGO_ENABLED=1
ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
    -trimpath \
    -ldflags="-s -w -buildid=" \
    -o ash ./cmd/ash

# ---- runtime image ----
FROM ubuntu:latest

LABEL org.opencontainers.image.title="ash"
LABEL org.opencontainers.image.description="minimal matrix message watcher and link extractor"
LABEL org.opencontainers.image.source="https://github.com/polarhive/ash"
LABEL org.opencontainers.image.licenses="GPL-3.0"

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    libolm3 \
    ca-certificates \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/ash /usr/local/bin/ash

USER 1001:1001

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD kill -0 1 2>/dev/null || exit 1

CMD ["ash"]
