//go:build ignore

// Keydris CO-RE attribution tracer.
//
// On every TCP socket transition into SYN_SENT (i.e. an outbound connect()),
// record the connection's 4-tuple -> {pid, cgroup_id}. Userspace looks the flow
// up by source endpoint to attribute an intercepted connection to its origin.
//
// Byte order note: in the inet_sock_set_state tracepoint, sport/dport are stored
// in host byte order, while saddr/daddr are raw network-order bytes. Userspace
// must match accordingly (validated by the spike).

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

char LICENSE[] SEC("license") = "GPL";

#define TCP_SYN_SENT 2
#define AF_INET 2

struct flow_key {
	__u32 saddr; // network byte order
	__u32 daddr; // network byte order
	__u16 sport; // host byte order
	__u16 dport; // host byte order
};

struct flow_origin {
	__u32 pid;
	__u64 cgroup_id;
};

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 16384);
	__type(key, struct flow_key);
	__type(value, struct flow_origin);
} flows SEC(".maps");

SEC("tracepoint/sock/inet_sock_set_state")
int handle_set_state(struct trace_event_raw_inet_sock_set_state *ctx)
{
	if (ctx->newstate != TCP_SYN_SENT)
		return 0;
	if (ctx->family != AF_INET)
		return 0;

	struct flow_key key = {};
	bpf_probe_read_kernel(&key.saddr, sizeof(key.saddr), ctx->saddr);
	bpf_probe_read_kernel(&key.daddr, sizeof(key.daddr), ctx->daddr);
	key.sport = ctx->sport;
	key.dport = ctx->dport;

	struct flow_origin origin = {};
	origin.pid = bpf_get_current_pid_tgid() >> 32;
	origin.cgroup_id = bpf_get_current_cgroup_id();

	bpf_map_update_elem(&flows, &key, &origin, BPF_ANY);
	return 0;
}
