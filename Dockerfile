# Single-stage Dockerfile to build and run the Go application on Ubuntu with Olm support

# Use official Golang image (Ubuntu-based) with Olm dev libraries for static linking
FROM golang:latest

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

# Download dependencies (cached if go.mod/go.sum unchanged)
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN go build -v -o ash .

# Copy the binary to /usr/local/bin
RUN cp ash /usr/local/bin/ash && chmod +x /usr/local/bin/ash

# Set the default command
CMD ["/usr/local/bin/ash"]