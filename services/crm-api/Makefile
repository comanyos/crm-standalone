.PHONY: build run test fmt vet tidy generate ci

build:
	go build -o bin/crm ./cmd/crm

run:
	go run ./cmd/crm

test:
	go test -race -cover ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

# Codegen for connect-go (requires buf + protoc-gen-go + protoc-gen-connect-go).
generate:
	buf generate

ci: fmt vet build test
