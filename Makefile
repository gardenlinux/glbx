.PHONY: all build fmt vet test clean

# bin/ is the canonical build output. Tests and the e2e driver consume the
# binaries from here rather than rebuilding via `go build`.
BIN_DIR := $(CURDIR)/bin

all: fmt vet build

# `build` is phony so `go build` runs unconditionally — Go's own build cache
# decides what actually needs recompiling.
build:
	@mkdir -p $(BIN_DIR)
	go build ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

# `-count=1` disables Go's test result cache so `make test` always exercises
# every package end-to-end.
test:
	go test -count=1 ./...

clean:
	rm -rf $(BIN_DIR)
