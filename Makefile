.PHONY: all build build_demo fmt vet test clean

# bin/ is the canonical build output. Tests and the e2e driver consume the
# binaries from here rather than rebuilding via `go build`.
BIN_DIR := $(CURDIR)/bin
GLBX_BIN := $(BIN_DIR)/glbx
STUB_BIN := $(BIN_DIR)/exec_env_stub

# Pin to the installed toolchain so the module's go directive never triggers a
# toolchain download mid-build.
export GOTOOLCHAIN := local

all: fmt vet build

# `build` is phony so `go build` runs unconditionally — Go's own build cache
# decides what actually needs recompiling.
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(GLBX_BIN) ./cmd/glbx
	go build -o $(STUB_BIN) ./cmd/exec_env_stub

# Demo binaries are not needed for tests; build them on demand.
build_demo:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/taskdemo ./cmd/taskdemo

fmt:
	go fmt ./...

vet:
	go vet ./...

# Tests consume the pre-built stub via GLBX_EXEC_ENV_STUB; tests that need it
# skip with a clear message when it is unset. `-count=1` disables the test
# result cache so every package is exercised end-to-end.
test: build
	GLBX_EXEC_ENV_STUB=$(STUB_BIN) go test -count=1 ./...

clean:
	rm -rf $(BIN_DIR)
