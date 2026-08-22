# Multi-stage build for the crm standalone module.
FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/crm ./cmd/crm

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 app
USER 10001:10001
WORKDIR /app
COPY --from=builder /out/crm /app/crm
EXPOSE 9431
ENTRYPOINT ["/app/crm"]
