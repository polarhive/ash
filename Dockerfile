# Use official Golang image (Ubuntu-based) with Olm dev libraries for static linking
FROM golang:latest AS builder

# Install build tools and Olm development libraries
RUN apt-get update && apt-get install -y \
    build-essential \
    libolm-dev

# Set Go environment variables for CGO
ENV CGO_ENABLED=1
ENV GOOS=linux
ENV GOARCH=amd64

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -v -o ash ./cmd/ash

# ---- runtime image ----
FROM ubuntu:latest

RUN apt-get update && apt-get install -y libolm3

COPY --from=builder /app/ash /usr/local/bin/ash

CMD ["ash"]
