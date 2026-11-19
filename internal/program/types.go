// Package program tracks PAT and PMT tables per program: which version is in
// force, which versions are merely announced, and what conflicts, removals or
// orphan tables have been observed.
package program

import (
	"fmt"
	"sort"

	"tsinspect/internal/psi"
)

// Table lifecycle statuses written to the report.
const (
	StatusApplied              = "applied"
	StatusAppliedVersionWrap   = "applied_version_wrap"
	StatusPending              = "pending"
	StatusSameVersionConflict  = "same_version_conflict"
	StatusRemoved              = "removed"
	StatusOrphanPMT            = "orphan_pmt"
	StatusIgnoredNextTable     = "ignored_next_table"
	StatusPMTProgramMismatch   = "pmt_program_mismatch"
	StatusInconsistentSections = "inconsistent_section_set"
	StatusPMTPIDChanged        = "pmt_pid_changed"
	StatusStaleVersion         = "stale_version_ignored"
)

// Limits keep hostile or damaged input from growing table state without bound.
const (
	MaxPrograms    = 4096
	MaxEvents      = 2048
	MaxUnboundPMTs = 64
	maxNotedEvents = 2048
)

type patCandidate struct {
	version  uint8
	sections [][]byte
	last     uint8
	complete bool
	merged   *psi.PAT
}

type pmtCandidate struct {
	version  uint8
	last     uint8
	sections map[uint8]*psi.PMT
	complete bool
	merged   *PMTState
}

type unboundPMT struct {
	program uint16
	version uint8
	pmt     *psi.PMT
}

// Stream is the effective declaration of one elementary stream.
type Stream struct {
	PID        uint16
	StreamType uint8
}

// PATState is the effective PAT.
type PATState struct {
	TransportStreamID uint16
	Version           uint8
	Programs          map[uint16]uint16 // program_number -> PMT PID
}

// PMTState is the effective, merged PMT of one program.
type PMTState struct {
	ProgramNumber uint16
	PID           uint16 // PMT PID
	Version       uint8
	PCRPID        uint16
	Streams       []Stream // sorted by PID
}

// Event explains one table lifecycle transition.
type Event struct {
	Kind             string // "pat" or "pmt"
	ProgramNumber    uint16
	PID              uint16
	Version          uint8
	Status           string
	Detail           string
	EffectiveVersion *uint8
}

// Manager owns all table state for one input stream. It is not safe for
// concurrent use.
type Manager struct {
	pat        *PATState
	patPending *patCandidate
	pmts       map[uint16]*PMTState
	pmtPending map[uint16]*pmtCandidate
	unbound    map[uint16]*unboundPMT
	unboundSeq []uint16

	patGeneration int
	patChanges    int
	pmtChanges    int
	orphanCount   int

	events        []Event
	droppedEvents int64
	noted         map[string]bool
}

// NewManager creates empty table state.
func NewManager() *Manager {
	return &Manager{
		pmts:       make(map[uint16]*PMTState),
		pmtPending: make(map[uint16]*pmtCandidate),
		unbound:    make(map[uint16]*unboundPMT),
		noted:      make(map[string]bool),
	}
}

// PAT returns the effective PAT, or nil if none has been applied yet.
func (m *Manager) PAT() *PATState { return m.pat }

// PMT returns the effective PMT for a program, or nil.
func (m *Manager) PMT(program uint16) *PMTState {
	if s, ok := m.pmts[program]; ok {
		return s
	}
	return nil
}

// ProgramForPMTPID resolves the program currently served by a PMT PID.
func (m *Manager) ProgramForPMTPID(pid uint16) (uint16, bool) {
	if m.pat == nil {
		return 0, false
	}
	for program, pmtPID := range m.pat.Programs {
		if pmtPID == pid {
			return program, true
		}
	}
	return 0, false
}

// PMTPIDs returns the PMT PIDs referenced by the effective PAT, sorted.
func (m *Manager) PMTPIDs() []uint16 {
	if m.pat == nil {
		return nil
	}
	out := make([]uint16, 0, len(m.pat.Programs))
	for _, pid := range m.pat.Programs {
		out = append(out, pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Programs returns effective program numbers in ascending order.
func (m *Manager) Programs() []uint16 {
	out := make([]uint16, 0, len(m.pmts))
	for p := range m.pmts {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Events returns lifecycle events in processing order.
func (m *Manager) Events() []Event { return append([]Event(nil), m.events...) }

// DroppedEvents counts events discarded after MaxEvents.
func (m *Manager) DroppedEvents() int64 { return m.droppedEvents }

// PATVersionChanges counts PAT version transitions that took effect.
func (m *Manager) PATVersionChanges() int { return m.patChanges }

// PMTVersionChanges counts PMT version transitions that took effect.
func (m *Manager) PMTVersionChanges() int { return m.pmtChanges }

// OrphanPMTs counts orphan PMT observations (deduplicated per PID/version).
func (m *Manager) OrphanPMTs() int { return m.orphanCount }

// PATGeneration increments whenever an effective PAT is applied; callers use
// it to refresh derived caches such as PSI PID filters.
func (m *Manager) PATGeneration() int { return m.patGeneration }

func (m *Manager) event(ev Event) {
	if len(m.events) >= MaxEvents {
		m.droppedEvents++
		return
	}
	m.events = append(m.events, ev)
}

func (m *Manager) noteOnce(key string, ev Event) {
	if m.noted[key] {
		return
	}
	if len(m.noted) >= maxNotedEvents {
		m.noted = make(map[string]bool)
	}
	m.noted[key] = true
	m.event(ev)
}

func effective(v uint8) *uint8 { return &v }

// versionRelation reports whether next is the same as, ahead of, or behind cur
// in the 5-bit version space. A modular delta below half the space is newer
// (31 -> 0 wraps to +1), and a larger delta is a delayed retransmission of an
// older table. A delta of exactly half the space is treated as stale because
// the ordering is genuinely ambiguous; keeping the effective table is safer
// than toggling between two candidates.
func versionRelation(cur, next uint8) int {
	switch delta := (next - cur) & 0x1f; {
	case delta == 0:
		return 0
	case delta < 16:
		return 1
	default:
		return -1
	}
}

func streamsSignature(s []Stream) string {
	b := make([]byte, 0, len(s)*3)
	for _, es := range s {
		b = append(b, byte(es.PID>>8), byte(es.PID), es.StreamType)
	}
	return string(b)
}

func sameStreams(a, b []Stream) bool { return streamsSignature(a) == streamsSignature(b) }

// ErrConflict reports a same-version table whose contents disagree with the
// effective one, or an internally inconsistent multi-section table.
type ErrConflict struct {
	Kind    string
	Program uint16
	Version uint8
	Detail  string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("%s version %d conflicts: %s", e.Kind, e.Version, e.Detail)
}
