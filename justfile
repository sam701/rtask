build:
  go build

run *args: build
  GO_LOG=debug ./boj8 {{args}}
