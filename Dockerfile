# Build stage — uses the official Go image; the modernc SQLite driver needs no
# CGO, so the final binary is fully static.
FROM golang:1.26-alpine AS build
WORKDIR /src

# Build identity, injected so GET /version identifies the running image.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown

# Cache dependencies separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath \
        -ldflags="-s -w \
          -X github.com/YLHhhh123/HollowTone-server/internal/version.Version=${VERSION} \
          -X github.com/YLHhhh123/HollowTone-server/internal/version.Commit=${COMMIT} \
          -X github.com/YLHhhh123/HollowTone-server/internal/version.BuildTime=${BUILD_TIME}" \
        -o /out/hollowtone-server ./cmd/hollowtone-server

# Runtime stage — scratch-level minimal image.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S hollowtone && adduser -S -G hollowtone hollowtone
WORKDIR /app
COPY --from=build /out/hollowtone-server /app/hollowtone-server

# Persisted data: SQLite DB + object store live under /data.
VOLUME ["/data"]
ENV HT_DATA_DIR=/data \
    HT_ADDR=:8080
EXPOSE 8080
USER hollowtone

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/hollowtone-server"]
