package ts

import (
	"fmt"

	"medpipe/internal/fault"
)

// PSIVersionEvent is the stable event code for a committed PSI version.
const PSIVersionEvent fault.Code = "psi_version_committed"

type sectionAssembly struct {
	tableID  uint8
	version  uint8
	last     uint8
	sections map[uint8]*Section
}

// PSIParser commits PAT/PMT tables only after every section of one version is
// present and every section has a correct CRC.
type PSIParser struct {
	pat       *PAT
	pmts      map[uint16]*PMT
	pending   map[string]*sectionAssembly
	events    []fault.Event
	Committed []fault.Event
}

// NewPSIParser creates an empty PSI parser.
func NewPSIParser() *PSIParser {
	return &PSIParser{
		pmts:    map[uint16]*PMT{},
		pending: map[string]*sectionAssembly{},
	}
}

// Events returns all PSI fault events.
func (p *PSIParser) Events() []fault.Event { return append([]fault.Event(nil), p.events...) }

// ActivePAT returns the last completely committed PAT, if any.
func (p *PSIParser) ActivePAT() *PAT { return p.pat }

// ActivePMTs returns committed PMTs keyed by their TS PID.
func (p *PSIParser) ActivePMTs() map[uint16]*PMT {
	out := make(map[uint16]*PMT, len(p.pmts))
	for k, v := range p.pmts {
		out[k] = v
	}
	return out
}

// Tracks returns all tracks from all active PMTs.
func (p *PSIParser) Tracks() []TrackDescription {
	var out []TrackDescription
	for _, pmt := range p.pmts {
		out = append(out, pmt.Tracks...)
	}
	return out
}

// SubmitSection accepts one complete PSI section for pid. A CRC or assembly
// failure never clears the previously active table.
func (p *PSIParser) SubmitSection(pid uint16, raw []byte) (committed bool, err *fault.Error) {
	s, perr := ParseSection(raw)
	if perr != nil {
		p.events = append(p.events, fault.Event{
			Code:     fault.ErrBadCRC,
			Severity: "corrupt",
			Stage:    "ts.psi",
			PID:      pid,
			Message:  perr.Message,
		})
		return false, perr
	}
	if !s.CurrentNext {
		return false, nil
	}

	key := fmt.Sprintf("%d:%d:%d", s.TableID, pid, s.Version)
	a := p.pending[key]
	if a == nil {
		a = &sectionAssembly{
			tableID:  s.TableID,
			version:  s.Version,
			last:     s.LastSection,
			sections: map[uint8]*Section{},
		}
		p.pending[key] = a
	}
	if s.LastSection != a.last {
		p.events = append(p.events, fault.Event{
			Code:     fault.ErrPSISection,
			Severity: "corrupt",
			Stage:    "ts.psi",
			PID:      pid,
			Message:  fmt.Sprintf("inconsistent last_section_number for version %d", s.Version),
		})
		return false, fault.New(fault.ErrPSISection, "ts.psi", 0, "inconsistent last section number")
	}
	a.sections[s.SectionNumber] = s

	for i := uint8(0); i <= a.last; i++ {
		if _, ok := a.sections[i]; !ok {
			return false, nil // keep old mapping and wait for missing section
		}
	}
	sections := make([]*Section, 0, a.last+1)
	for i := uint8(0); i <= a.last; i++ {
		sections = append(sections, a.sections[i])
	}

	switch s.TableID {
	case 0x00:
		pat, perr := ParsePATPayload(sections)
		if perr != nil {
			p.events = append(p.events, fault.Event{Code: perr.Code, Severity: "corrupt", Stage: "ts.psi", PID: pid, Message: perr.Message})
			return false, perr
		}
		oldVersion := uint8(255)
		if p.pat != nil {
			oldVersion = p.pat.Version
		}
		p.pat = pat
		p.Committed = append(p.Committed, fault.Event{
			Code: PSIVersionEvent, Severity: "info", Stage: "ts.pat", PID: 0,
			Message: fmt.Sprintf("PAT version %d committed (previous=%d)", pat.Version, oldVersion),
		})
	case 0x02:
		pmt, perr := ParsePMTPayload(pid, sections[0])
		if perr != nil {
			p.events = append(p.events, fault.Event{Code: perr.Code, Severity: "corrupt", Stage: "ts.psi", PID: pid, Message: perr.Message})
			return false, perr
		}
		p.pmts[pid] = pmt
		p.Committed = append(p.Committed, fault.Event{
			Code: PSIVersionEvent, Severity: "info", Stage: "ts.pmt", PID: pid,
			Message: fmt.Sprintf("PMT version %d committed", pmt.Version),
		})
	default:
		return false, fault.New(fault.ErrPSISection, "ts.psi", 0, fmt.Sprintf("unsupported table id 0x%02x", s.TableID))
	}
	delete(p.pending, key)
	return true, nil
}

// MissingVersions returns events for version assemblies that never became
// complete. It does not alter active tables.
func (p *PSIParser) MissingVersions() []fault.Event {
	var out []fault.Event
	for _, a := range p.pending {
		missing := 0
		for i := uint8(0); i <= a.last; i++ {
			if _, ok := a.sections[i]; !ok {
				missing++
			}
		}
		if missing > 0 {
			out = append(out, fault.Event{
				Code:     fault.ErrPSIVersion,
				Severity: "gap",
				Stage:    "ts.psi",
				Message:  fmt.Sprintf("table=0x%02x version=%d missing %d section(s); active mapping retained", a.tableID, a.version, missing),
			})
		}
	}
	return out
}
