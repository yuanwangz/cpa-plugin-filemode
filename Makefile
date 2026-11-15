# Build the file-mode plugin as a C-ABI shared library (CGO required).
# CC can be any C compiler; in a sandbox without gcc, `zig cc` works:
#   make CC="zig cc -target aarch64-linux-gnu"
GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
EXT    := $(if $(filter darwin,$(GOOS)),dylib,$(if $(filter windows,$(GOOS)),dll,so))
OUT    ?= build/filemode.$(EXT)

.PHONY: build test e2e clean
build:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(OUT) .

test:
	CGO_ENABLED=0 go test ./internal/...

# make e2e CPA=/path/to/cliproxyapi-binary (built with CGO)
e2e: build
	test/e2e/run.sh $(CPA) $(abspath $(OUT)) $(abspath build/e2e-run)

clean:
	rm -rf build
