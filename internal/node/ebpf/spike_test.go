//go:build linux && ebpf

package ebpf

import (
	"testing"
	"time"
)

// TestSpike validates the eBPF join end-to-end: load + attach the tracer, let
// some outbound connections happen, then dump the flow map. Run on the VM:
//
//	make ebpf-spike   # go test -tags ebpf -run Spike -v ./internal/node/ebpf
//
// In another shell, generate traffic (e.g. `curl http://example.com`) while it
// sleeps; the test should print captured 4-tuple -> {pid, cgroup_id} entries.
func TestSpike(t *testing.T) {
	tr, err := Load()
	if err != nil {
		t.Fatalf("load tracer: %v", err)
	}
	defer tr.Close()

	t.Log("tracer attached to inet_sock_set_state; capturing for 5s...")
	time.Sleep(5 * time.Second)

	var (
		key   tracerFlowKey
		val   tracerFlowOrigin
		count int
	)
	it := tr.objs.Flows.Iterate()
	for it.Next(&key, &val) {
		count++
		t.Logf("flow saddr=%#08x daddr=%#08x sport=%d dport=%d -> pid=%d cgroup_id=%d",
			key.Saddr, key.Daddr, key.Sport, key.Dport, val.Pid, val.CgroupId)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate flows: %v", err)
	}
	t.Logf("captured %d flows", count)
}
