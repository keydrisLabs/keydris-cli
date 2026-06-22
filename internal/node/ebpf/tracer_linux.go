//go:build linux && ebpf

package ebpf

import (
	"fmt"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// Tracer is a loaded + attached attribution tracer.
type Tracer struct {
	objs tracerObjects
	lnk  link.Link
}

// FlowOrigin is the userspace view of a recorded connection origin.
type FlowOrigin struct {
	PID      uint32
	CgroupID uint64
}

// Load loads the CO-RE tracer and attaches it to inet_sock_set_state.
func Load() (*Tracer, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove memlock: %w", err)
	}
	t := &Tracer{}
	if err := loadTracerObjects(&t.objs, nil); err != nil {
		return nil, fmt.Errorf("load objects: %w", err)
	}
	l, err := link.Tracepoint("sock", "inet_sock_set_state", t.objs.HandleSetState, nil)
	if err != nil {
		_ = t.objs.Close()
		return nil, fmt.Errorf("attach tracepoint: %w", err)
	}
	t.lnk = l
	return t, nil
}

// LookupBySource returns the origin of the flow whose source endpoint matches
// (saddr in network byte order, sport in host byte order).
func (t *Tracer) LookupBySource(saddr uint32, sport uint16) (FlowOrigin, bool) {
	var (
		key tracerFlowKey
		val tracerFlowOrigin
	)
	it := t.objs.Flows.Iterate()
	for it.Next(&key, &val) {
		if key.Saddr == saddr && key.Sport == sport {
			return FlowOrigin{PID: val.Pid, CgroupID: val.CgroupId}, true
		}
	}
	return FlowOrigin{}, false
}

// Close detaches and releases the tracer.
func (t *Tracer) Close() error {
	if t.lnk != nil {
		_ = t.lnk.Close()
	}
	return t.objs.Close()
}
