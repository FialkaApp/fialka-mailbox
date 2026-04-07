FROM golang:1.23-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o fialka .

# ── Runtime image ─────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache tor ca-certificates tzdata && \
    addgroup -S fialka && adduser -S fialka -G fialka

WORKDIR /app
COPY --from=builder /build/fialka .
COPY config.toml.example config.toml.example

RUN mkdir -p /data && chown fialka:fialka /data

USER fialka
VOLUME ["/data"]
EXPOSE 8765

ENTRYPOINT ["./fialka"]
CMD ["start"]
