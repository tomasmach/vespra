FROM golang:1.24-bookworm AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -tags sqlite_fts5 -o vespra .
RUN CGO_ENABLED=1 GOOS=linux go build -tags sqlite_fts5 -o vespra-runnerd ./cmd/vespra-runnerd

FROM debian:bookworm-slim AS bash-job
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    git \
    jq \
    ripgrep \
    && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --uid 1000 sandbox && mkdir -p /workspace && chown sandbox:sandbox /workspace
WORKDIR /workspace
USER sandbox
CMD ["bash"]

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /build/vespra .
COPY --from=builder /build/vespra-runnerd .
EXPOSE 8080
VOLUME ["/data", "/config"]
CMD ["/app/vespra", "--config", "/config/config.toml"]
