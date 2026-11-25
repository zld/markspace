// Package recovery manages per-representation recovery. Each layer is restored
// independently: a failed layer recomputes its boundaries from the shared
// clock and is cross-checked against the still-available layers. Both the
// pre-recovery and post-recovery conclusions are retained for the report.
package recovery

import (
	"fmt"
	"sync"

	"packcheck/internal/model"
	"packcheck/internal/segment"
)

// Verdict is one layer's conclusion at one point in time.
type Verdict string

// Recovery verdicts.
const (
	VerdictPassed     Verdict = "passed"
	VerdictFailed     Verdict = "failed"
	VerdictRecovered  Verdict = "recovered"
	VerdictQuarantined Verdict = "quarantined"
)

// LayerState holds the full audit trail for one representation.
type LayerState struct {
	Representation string
	Before         Verdict
	After          Verdict
	Healthy        bool
	// Recomputed is the boundary signature derived from the shared clock
	// during recovery; nil when recovery never ran.
	Recomputed []uint32
	// ReferenceTimestamps is the healthy-layer signature compared against.
	ReferenceTimestamps []uint32
	Issues              []model.Issue
}

// Manager coordinates independent per-layer recovery.
type Manager struct {
	mu     sync.Mutex
	states map[string]*LayerState
	order  []string
	log    model.IssueLog
}

// NewManager creates an empty recovery manager.
func NewManager() *Manager {
	return &Manager{states: map[string]*LayerState{}}
}

// Register records a layer together with its pre-recovery verdict.
func (m *Manager) Register(name string, healthy bool) *LayerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.states[name]; !seen {
		m.order = append(m.order, name)
	}
	before := VerdictPassed
	if !healthy {
		before = VerdictFailed
	}
	st := &LayerState{Representation: name, Before: before, Healthy: healthy}
	if healthy {
		st.After = VerdictPassed
	}
	m.states[name] = st
	return st
}

// Recover attempts an independent recovery of one failed layer. The shared
// clock supplies the recomputed boundary signature; healthy layers supply the
// reference. A matching signature yields "recovered", otherwise the layer is
// quarantined and excluded from the rollback manifest.
func (m *Manager) Recover(name string, sharedClock []uint32, healthySigs []segment.LayerSignature) *LayerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[name]
	if !ok {
		return nil
	}
	if st.Healthy {
		st.After = VerdictPassed
		return st
	}
	st.Recomputed = append([]uint32(nil), sharedClock...)
	if len(healthySigs) == 0 {
		st.After = VerdictQuarantined
		m.log.Add(model.Issue{
			Code: model.CodeTruncatedLayer, Severity: model.SeverityError,
			Layer: model.LayerRecovery, Representation: name,
			Message: "no healthy reference layer available for cross-check",
		})
		st.Issues = append(st.Issues, m.log.All()[len(m.log.All())-1])
		return st
	}
	reference := healthySigs[0].Timestamps
	st.ReferenceTimestamps = reference
	if equal(sharedClock, reference) {
		st.After = VerdictRecovered
		st.Healthy = true
		return st
	}
	st.After = VerdictQuarantined
	m.log.Add(model.Issue{
		Code: model.CodeTruncatedLayer, Severity: model.SeverityError,
		Layer: model.LayerRecovery, Representation: name,
		Message: fmt.Sprintf("recomputed %d boundaries, reference has %d; mismatch retained",
			len(sharedClock), len(reference)),
	})
	st.Issues = append(st.Issues, m.log.All()[len(m.log.All())-1])
	return st
}

// States returns every layer state in registration order.
func (m *Manager) States() []*LayerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*LayerState, 0, len(m.order))
	for _, n := range m.order {
		out = append(out, m.states[n])
	}
	return out
}

// Eligible returns representations allowed into the rollback manifest:
// layers that passed or recovered and carry no error-severity issue.
func (m *Manager) Eligible() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, n := range m.order {
		st := m.states[n]
		if (st.After == VerdictPassed || st.After == VerdictRecovered) && st.Healthy {
			out = append(out, n)
		}
	}
	return out
}

// Issues exposes the recovery issue log.
func (m *Manager) Issues() *model.IssueLog { return &m.log }

func equal(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
