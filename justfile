build:
  go build

run *args: build
  GO_LOG=debug ./rtask {{args}}

# Download and verify dependencies
deps:
  go mod download
  go mod verify

# Run tests with race detection and coverage
test:
  go test -v -race -timeout 5m -coverprofile=coverage.out -covermode=atomic
  go tool cover -func=coverage.out

# Run linters
lint:
  go vet ./...
  gofmt -l .
  staticcheck

# Check code formatting (fail if not formatted)
fmt-check:
  #!/usr/bin/env bash
  set -euo pipefail
  if [ -n "$(gofmt -l .)" ]; then
    echo "Go code is not formatted:"
    gofmt -d .
    exit 1
  fi

# Full CI check (what runs in GitHub Actions)
ci: deps lint fmt-check test
