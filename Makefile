GO   ?= go
BIN  ?= bin
DIST ?= dist
PREFIX ?= /usr/local

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/keydrisLabs/keydris-cli/internal/cli.Version=$(VERSION)
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

# Release distribution: which S3 bucket + channel to publish to, and the
# CloudFront distribution to invalidate (optional).
CHANNEL         ?= stable
S3_BUCKET       ?=
DISTRIBUTION_ID ?=

# Keep the build cache inside the repo so it works in restricted sandboxes.
export GOCACHE ?= $(CURDIR)/.gobuild

.PHONY: build install vet test clean dist npm-stage release ebpf-vmlinux ebpf-gen ebpf-build ebpf-spike

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/keydris ./cmd/keydris

install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BIN)/keydris $(PREFIX)/bin/keydris

# Cross-compile the release matrix (static, stdlib-only) + checksums into $(DIST).
# Windows targets get a .exe suffix so the artifact name is runnable as-is.
dist:
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=; \
	  [ "$$os" = windows ] && ext=.exe; \
	  echo "  building keydris-$$os-$$arch$$ext ($(VERSION))"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	    $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/keydris-$$os-$$arch$$ext ./cmd/keydris || exit 1; \
	done
	@cd $(DIST) && (command -v sha256sum >/dev/null 2>&1 && sha256sum keydris-* || shasum -a 256 keydris-*) > SHA256SUMS
	@echo "  wrote $(DIST)/SHA256SUMS"

# Stage the built binaries from $(DIST) into the npm platform packages' bin/
# directories, mapping GOOS/GOARCH -> npm os/cpu naming (amd64->x64, windows->win32).
# Run `make dist` first. Used for local testing and by the npm release workflow.
npm-stage:
	@test -d $(DIST) || { echo "no $(DIST)/ — run 'make dist' first"; exit 1; }
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=; \
	  [ "$$os" = windows ] && ext=.exe; \
	  npmos=$$os; [ "$$os" = windows ] && npmos=win32; \
	  npmarch=$$arch; [ "$$arch" = amd64 ] && npmarch=x64; \
	  dst=npm/packages/platform-$$npmos-$$npmarch/bin/keydris$$ext; \
	  cp $(DIST)/keydris-$$os-$$arch$$ext $$dst || exit 1; \
	  [ "$$os" != windows ] && chmod 0755 $$dst; \
	  echo "  staged $$dst"; \
	done

# Publish $(DIST) + install.sh (+ dev config) to S3. Needs S3_BUCKET and AWS creds.
release: dist
	@test -n "$(S3_BUCKET)" || { echo "set S3_BUCKET=<your-bucket>"; exit 1; }
	aws s3 sync $(DIST)/ s3://$(S3_BUCKET)/keydris-cli/$(CHANNEL)/$(VERSION)/
	aws s3 sync $(DIST)/ s3://$(S3_BUCKET)/keydris-cli/$(CHANNEL)/latest/ --cache-control max-age=60
	aws s3 cp install.sh s3://$(S3_BUCKET)/keydris-cli/install.sh --cache-control max-age=60
	@if [ "$(CHANNEL)" = dev ]; then \
	  aws s3 cp deploy/dev/keydris.toml s3://$(S3_BUCKET)/keydris-cli/dev/$(VERSION)/keydris.toml --cache-control max-age=60; \
	  aws s3 cp deploy/dev/keydris.toml s3://$(S3_BUCKET)/keydris-cli/dev/latest/keydris.toml --cache-control max-age=60; \
	fi
	@if [ -n "$(DISTRIBUTION_ID)" ]; then \
	  aws cloudfront create-invalidation --distribution-id $(DISTRIBUTION_ID) --paths "/keydris-cli/install.sh" "/keydris-cli/$(CHANNEL)/latest/*"; \
	fi

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

clean:
	rm -rf $(BIN) $(DIST) .gobuild

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
