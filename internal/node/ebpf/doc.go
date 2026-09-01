// Package ebpf contains the Keydris CO-RE attribution tracer.
//
// The eBPF program (tracer.bpf.c) attaches to the inet_sock_set_state
// tracepoint and, on each TCP SYN_SENT transition, records {4-tuple} ->
// {pid, cgroup_id} in a BPF map. The userspace loader (build tag "ebpf")
// reads that map to attribute an intercepted connection to its origin
// process/cgroup race-free, which the /proc resolver in internal/node/attest
// approximates without a kernel program.
//
// Build path (Linux VM only): `make ebpf-vmlinux && make ebpf-gen` generates the
// Go bindings via bpf2go (requires clang/llvm + kernel BTF), then build with
// `-tags ebpf`. The default build excludes all of this and uses /proc.
package ebpf
