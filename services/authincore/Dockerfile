# Stage 1: Build the hosted login UI that the Go binary embeds.
FROM node:22-alpine AS hosted-ui-builder
WORKDIR /ui
COPY hosted-ui/package.json hosted-ui/package-lock.json ./
RUN npm ci
COPY hosted-ui/ ./
RUN npm run build

# Stage 2: Build the service.
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
COPY third_party/platform-contract ./third_party/platform-contract
RUN go mod download

COPY . ./
COPY --from=hosted-ui-builder /ui/dist ./hosted-ui/dist
ARG ENTERPRISE=false
RUN if [ "$ENTERPRISE" = "true" ]; then \
	  CGO_ENABLED=0 GOOS=linux go build -tags=enterprise -trimpath -ldflags="-s -w" -o /bin/authincore ./cmd/authincore; \
	else \
	  CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/authincore ./cmd/authincore; \
	fi
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/authinctl ./cmd/authinctl

# Stage 3: Minimal runtime
FROM scratch

LABEL org.opencontainers.image.source="https://github.com/authincore/authincore"
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/authincore /authincore
COPY --from=builder /bin/authinctl /authinctl

USER 65532:65532

EXPOSE 8080
ENTRYPOINT ["/authincore"]
