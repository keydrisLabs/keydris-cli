GO   ?= go
BIN  ?= bin
PREFIX ?= /usr/local

# Keep the build cache inside the repo so it works in restricted sandboxes.
export GOCACHE ?= $(CURDIR)/.gobuild

.PHONY: build install vet test clean ebpf-vmlinux ebpf-gen ebpf-build ebpf-spike

build:
	$(GO) build -o $(BIN)/keydris ./cmd/keydris

install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BIN)/keydris $(PREFIX)/bin/keydris

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

clean:
	rm -rf $(BIN) .gobuild

# --- eBPF (Linux only, behind the `ebpf` build tag) -------------------------
# Race-free connection->cgroup attribution. Requires a Linux host with bpftool
# and clang; artifacts are generated, not committed (see .gitignore).
ebpf-vmlinux:
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > internal/node/ebpf/vmlinux.h

ebpf-gen:
	$(GO) generate ./internal/node/ebpf

ebpf-build:
	$(GO) build -tags ebpf ./...

ebpf-spike:
	sudo $(GO) test -tags ebpf -run Spike -v ./internal/node/ebpf
