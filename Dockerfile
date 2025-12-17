# --- Stage 1: Builder ---
FROM golang:1.25-alpine AS builder

# Install git (required for fetching some Go dependencies)
RUN apk add --no-cache git

WORKDIR /app

# Cache dependencies first (better build speed)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary (CGO_ENABLED=0 for static linking)
RUN CGO_ENABLED=0 GOOS=linux go build -o k8s-inspector .

# --- Stage 2: Runtime ---
FROM alpine:latest

# Install CA certificates (Required for HTTPS calls to K8s APIs)
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the binary from builder
COPY --from=builder /app/k8s-inspector .

# Copy required assets (Templates & Static files)
COPY --from=builder /app/views ./views

# Create directory for kubeconfigs (Volume mount point)
RUN mkdir -p /root/kubeconfig

# Expose the application port
EXPOSE 8080

# Run the application
CMD ["./k8s-inspector"]