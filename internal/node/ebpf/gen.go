//go:build linux

package ebpf

// bpf2go compiles tracer.bpf.c to a CO-RE object and generates the Go bindings
// (tracer_bpfel.go + .o). Requires clang/llvm and a vmlinux.h (see `make
// ebpf-vmlinux`). Run via `make ebpf-gen` (i.e. `go generate ./internal/node/ebpf`).
//
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -target bpfel -type flow_key -type flow_origin Tracer tracer.bpf.c -- -I.
