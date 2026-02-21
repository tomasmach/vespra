FROM golang:1.24-bookworm AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o mnemon-bot .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /build/mnemon-bot .
ENV MNEMON_DB_PATH=/data/mnemon.db
EXPOSE 8080
VOLUME ["/data", "/config"]
CMD ["/app/mnemon-bot", "--config", "/config/config.toml"]
