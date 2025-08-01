build:
  go build

run *args: build
  GO_LOG=debug ./rtask {{args}}
