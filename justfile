[private]
default:
    @just --list

# Run every check CI runs: format, tidy, vet, build, test, lint
check: fmt-check tidy-check vet build test privacy lint

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

# Fail when go.mod or go.sum is not tidy; the release build runs go mod tidy
# and would otherwise compile from files that differ from the tag
tidy-check:
    go mod tidy -diff

vet:
    go vet ./...

build:
    CGO_ENABLED=0 go build ./...

# Build the binary into ./bin
install:
    CGO_ENABLED=0 go build -o ./bin/ ./cmd/baum

# Fail when a tracked file mentions private monorepo or developer-machine material
privacy:
    go test ./internal/privacyguard/

test *args:
    go test -race ./... {{ args }}

lint:
    golangci-lint run ./...

# Rebuild the committed viewer bundle and the precompiled tile validators
viewer-build:
    sh scripts/viewer-build.sh

# Fail when the committed viewer bundle or validators differ from a fresh build
viewer-fresh:
    sh scripts/viewer-fresh.sh

# Build release archives for every target locally without publishing; the
# goreleaser before-hook runs viewer-fresh first
release-snapshot:
    go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean

# Check every archive release-snapshot built: CGO off and the viewer bundle embedded
release-verify:
    bash scripts/release-verify.sh

# Install baum from a module-proxy-shaped source tree with no Node, then smoke test it
clean-install:
    bash scripts/clean-install.sh

# Regenerate third_party/go.txt (the Go modules' licences) and assemble
# THIRD_PARTY_NOTICES.txt from it and the npm and Rust parts. The npm and Rust
# parts need their own containers: from the monorepo root run
# "just third-party-notices" instead.
notices-go:
    sh scripts/third-party-notices.sh go
    sh scripts/third-party-notices.sh assemble

# Fail when the committed Go notices or an assembled notices file is stale
notices-check:
    sh scripts/third-party-notices.sh check
