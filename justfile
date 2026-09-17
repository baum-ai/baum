[private]
default:
    @just --list

# Run every check CI runs: format, vet, build, test, lint
check: fmt-check vet build test lint

# Fail on unformatted files
fmt-check:
    #!/usr/bin/env sh
    files=$(gofmt -l .)
    if [ -n "$files" ]; then
        echo "unformatted files:" >&2
        echo "$files" >&2
        exit 1
    fi

fmt:
    gofmt -w .

vet:
    go vet ./...

build:
    CGO_ENABLED=0 go build ./...

# Build the binary into ./bin
install:
    CGO_ENABLED=0 go build -o ./bin/ ./cmd/reviewpal

test *args:
    go test -race ./... {{ args }}

lint:
    golangci-lint run ./...

# Re-fetch the vendored review schema from its published URL
schema-refresh:
    curl -fsSL https://static.recountable.ai/schemas/review/1.json -o schema/review-1.json
    go test ./schema/...

# Build release archives for every target locally without publishing
release-snapshot:
    go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
