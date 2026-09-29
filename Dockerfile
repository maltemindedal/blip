# Multi-stage Dockerfile for the Blip server.
# Produces a minimal, non-root production image.

# Stage 1: build
FROM golang:1.26.8-alpine AS builder

WORKDIR /build

# Dependencies are copied first so the download layer is cached independently
# of source changes.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# -trimpath strips filesystem paths; -s -w drops the symbol and DWARF tables.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o blip \
    ./cmd/server

# Stage 2: runtime
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -g 1000 blip && \
    adduser -D -u 1000 -G blip blip

WORKDIR /app
COPY --from=builder --chown=blip:blip /build/blip ./blip

USER blip

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/ || exit 1

ENTRYPOINT ["/app/blip"]
