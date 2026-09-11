# Multi-stage build for the crm standalone module.
FROM golang:1.26.8-alpine3.24@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/crm ./cmd/crm

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 app
USER 10001:10001
WORKDIR /app
COPY --from=builder /out/crm /app/crm
EXPOSE 9431
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:9431/healthz || exit 1
ENTRYPOINT ["/app/crm"]
