// Package analyze wires the transport packet reader to the PSI assembler,
// program map and continuity tracker and produces one auditable summary of a
// stream. All ordering follows the input packet order.
package analyze

import (
	"errors"
	"io"

	"github.com/asticode/go-astits"

	"github.com/example/tsaudit/internal/continuity"
	"github.com/example/tsaudit/internal/program"
	"github.com/example/tsaudit/internal/psi"
	"github.com/example/tsaudit/internal/tsreader"
)

// Exit codes reported through Summary.ExitCode.
const (
	ExitOK               = 0 // clean audit
	ExitStructural       = 2 // unreadable input or unusable structure
	ExitStrictContinuity = 3 // strict mode found continuity gaps
)

// TransportStats covers framing-level findings.
type TransportStats struct {
	TotalPackets          int64 `json:"total_packets"`
	SyncErrors            int64 `json:"sync_errors"`
	TruncatedTailBytes    int   `json:"truncated_tail_bytes"`
	TransportErrorPackets int64 `json:"transport_error_packets"`
}

// PIDCounters are the per-PID packet counts required by the audit report.
type PIDCounters struct {
	PID            uint16 `json:"pid"`
	TotalPackets   int64  `json:"total_packets"`
	ValidPackets   int    `json:"valid_packets"`
	Duplicates     int    `json:"duplicate_packets"`
	ContinuityGaps int    `json:"continuity_gaps"`
	GapEvents      int    `json:"gap_events"`
	ErrorPackets   int    `json:"error_packets"`
}

// Summary is the stable, serialisable result of an audit.
type Summary struct {
	Source            string                  `json:"source"`
	Transport         TransportStats          `json:"transport"`
	PATVersionChanges int                     `json:"pat_version_changes"`
	PMTVersionChanges int                     `json:"pmt_version_changes"`
	PATWraps          int                     `json:"pat_wraps"`
	PMTWraps          int                     `json:"pmt_wraps"`
	PublishedVersions []psi.Publication       `json:"published_versions"`
	Mappings          []program.MappingRecord `json:"mappings"`
	PIDCounters       []PIDCounters           `json:"pid_counters"`
	CorruptSections   []psi.SectionCandidate  `json:"corrupt_sections"`
	HasCompletePAT    bool                    `json:"has_complete_pat"`
	MissingPMTs       []uint16                `json:"missing_pmts"`
	StructuralErrors  []string                `json:"structural_errors"`
	ExitCode          int                     `json:"exit_code"`
}

// Analyzer owns the pipeline state for one input stream.
type Analyzer struct {
	source    string
	reader    *tsreader.Reader
	assembler *psi.Assembler
	progs     *program.Map
	tracker   *continuity.Tracker
	transport TransportStats

	// rawPID counts every packet observed per PID before any filtering.
	rawPID map[uint16]int64
	// pmtPIDsKnown tracks PIDs identified by a published PAT as PMT carriers.
	pmtPIDsKnown map[uint16]bool
	psiPIDs      map[uint16]bool
	errs         []string
}

// New creates an analyzer over r. source is used for display only.
func New(source string, r io.Reader) *Analyzer {
	a := &Analyzer{
		source:       source,
		reader:       tsreader.NewReader(r),
		assembler:    psi.NewAssembler(),
		progs:        program.New(),
		tracker:      continuity.New(),
		rawPID:       map[uint16]int64{},
		pmtPIDsKnown: map[uint16]bool{},
		psiPIDs:      map[uint16]bool{psi.PIDPAT: true},
	}
	a.assembler.OnPublication = a.onPublication
	return a
}

// Run consumes every packet and returns the completed summary. strict maps
// any continuity gap onto ExitStrictContinuity instead of a clean exit.
func (a *Analyzer) Run(strict bool) (*Summary, error) {
	for {
		pkt, err := a.reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, tsreader.ErrTruncated) {
			a.transport.TruncatedTailBytes = a.reader.TruncatedBytes()
			a.errs = append(a.errs, err.Error())
			break
		}
		if errors.Is(err, tsreader.ErrSyncByte) {
			a.transport.SyncErrors++
			a.errs = append(a.errs, err.Error())
		} else if err != nil {
			return nil, err
		}
		a.feed(pkt)
	}
	return a.buildSummary(strict), nil
}

// feed routes one framed packet through the pipeline stages.
func (a *Analyzer) feed(pkt *tsreader.Packet) {
	a.transport.TotalPackets++
	h := pkt.Header
	a.rawPID[h.PID]++

	transportError := h.TransportErrorIndicator || !pkt.SyncOK
	if h.TransportErrorIndicator {
		a.transport.TransportErrorPackets++
	}

	a.tracker.Observe(h.PID, h.ContinuityCounter, h.HasPayload, transportError)

	if transportError {
		return // damaged frames never reach PSI reassembly
	}
	if !h.HasPayload {
		return
	}

	// Known elementary PIDs carry PES, not PSI: never send their PUSI packets
	// to section reassembly. Everything else with a section start, plus known
	// PSI continuation packets, goes to the assembler, which rejects
	// unsupported table ids during validation.
	if a.isElementary(h.PID) {
		return
	}
	if h.PayloadUnitStartIndicator || h.PID == psi.PIDPAT || a.pmtPIDsKnown[h.PID] {
		a.feedPSI(h, pkt.Payload)
	}
}

// isElementary reports whether pid is currently mapped as an elementary
// stream by any published PMT.
func (a *Analyzer) isElementary(pid uint16) bool {
	return a.progs.IsElementaryPID(pid)
}

// feedPSI forwards a PSI payload to the shared section assembler. The
// tsreader has already removed the pointer_field, so payload[0] is the
// table id.
func (a *Analyzer) feedPSI(h astits.PacketHeader, payload []byte) {
	if len(payload) == 0 {
		return
	}
	a.assembler.FeedPayload(h.PID, h.PayloadUnitStartIndicator, payload)
}

// onPublication applies each fully validated table to the program map.
func (a *Analyzer) onPublication(pub psi.Publication) {
	switch pub.TableID {
	case psi.TableIDPAT:
		a.progs.ApplyPAT(pub.PAT)
		for _, pmtPID := range pub.PAT.Programs {
			a.pmtPIDsKnown[pmtPID] = true
			a.psiPIDs[pmtPID] = true
		}
	case psi.TableIDPMT:
		a.progs.ApplyPMT(pub.PMT, a.transport.TotalPackets-1)
	}
}
