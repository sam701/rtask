build:
  go build

run *args: build
  GO_LOG=debug ./t8sk {{args}}
