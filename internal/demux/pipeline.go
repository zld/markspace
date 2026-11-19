// Package demux wires the packet reader, PSI reassembly, program state,
// continuity accounting and slicing into one pass over the transport stream.
package demux

import (
	"errors"
	"io"

	"tsinspect/internal/metrics"
	"tsinspect/internal/program"
	"tsinspect/internal/psi"
	"tsinspect/internal/transport"
)

const (
	patPID = 0x0000
	// MaxPSIPIDs bounds the PSI reassembler table. PIDs outside the PAT/PMT
	// set plus these reserved ones never hold PSI for this tool.
	MaxPSIPIDs = 512
)

// ErrSectionIntegrity aggregates PSI failures (CRC, length, syntax). It makes
// the process exit with the input-error code even though inspection continued.
var ErrSectionIntegrity = errors.New("demux: PSI section integrity errors")

// Config controls one inspection pass.
type Config struct {
	Program          int // 0 selects all programs for slicing
	MaxBufferPackets int // packet cache budget before a pending program flushes
	// SliceToDisk enables file output. SliceEnabled is derived from Program.
	SliceToDisk bool
}

// Result is the reproducible summary of one pass.
type Result struct {
	Packets       int64
	Bytes         int64
	Metrics       *metrics.Tracker
	Programs      *program.Manager
	Slice         SliceStats
	CRCErrors     int64
	SectionErrors int64
	TableErrors   int64
	IgnoredTables int64
}

// Pipeline is a single-use inspector over one input.
type Pipeline struct {
	cfg      Config
	reader   *transport.Reader
	tracker  *metrics.Tracker
	progs    *program.Manager
	pat      *psi.Assembler
	pmts     map[uint16]*psi.Assembler
	slicer   *Slicer
	res      Result
	psiPIDs  map[uint16]bool
	verdicts map[uint16]metrics.Verdict
	patGen   int
}

// NewPipeline creates an inspector. out may be nil when slicing is disabled.
func NewPipeline(r io.Reader, cfg Config, out io.Writer) *Pipeline {
	p := &Pipeline{
		cfg:      cfg,
		reader:   transport.NewReader(r),
		tracker:  metrics.NewTracker(),
		progs:    program.NewManager(),
		pat:      psi.NewAssembler(),
		pmts:     make(map[uint16]*psi.Assembler),
		psiPIDs:  map[uint16]bool{patPID: true},
		verdicts: make(map[uint16]metrics.Verdict),
	}
	var w io.Writer
	if cfg.SliceToDisk {
		w = out
	}
	p.slicer = NewSlicer(w, uint16(cfg.Program), cfg.MaxBufferPackets, cfg.Program > 0)
	return p
}

// Run consumes the whole input. It returns the summary and a terminal error
// only for input-level failures (short tail, unreadable input, write error).
func (p *Pipeline) Run() (*Result, error) {
	for {
		pkt, err := p.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			p.finalize()
			return &p.res, err
		}
		p.res.Packets++
		p.res.Bytes += transport.PacketSize
		p.handle(pkt)
	}
	err := p.slicer.Close()
	p.finalize()
	return &p.res, err
}

func (p *Pipeline) finalize() {
	p.res.Metrics = p.tracker
	p.res.Programs = p.progs
	p.res.Slice = p.slicer.Stats()
}

// SliceError reports a failure writing the slice output.
func (p *Pipeline) SliceError() error { return p.slicer.Err() }

// IntegrityError reports PSI CRC/length failures, which the CLI maps to the
// input-error exit code. Parsing still continues so the report is complete.
func (p *Pipeline) IntegrityError() error {
	if p.res.CRCErrors == 0 && p.res.SectionErrors == 0 && p.res.TableErrors == 0 {
		return nil
	}
	return ErrSectionIntegrity
}

func (p *Pipeline) handle(pkt *transport.Packet) {
	verdict := p.tracker.Observe(pkt.PID, pkt.CC, pkt.HasPay, pkt.AFOnly, pkt.Discontinuity, pkt.TEI)
	p.verdicts[pkt.PID] = verdict

	// Damaged or discontinuous payloads never feed PSI reassembly: a lost
	// packet mid-section would otherwise splice two unrelated halves. Only a
	// packet that starts a new section may resume parsing after a break.
	feedPSI := pkt.HasPay && !pkt.TEI && !pkt.Scrambled
	if pkt.HasPay && pkt.PID <= 0x1fff {
		switch verdict {
		case metrics.VerdictOK, metrics.VerdictFirst:
		case metrics.VerdictDiscontinuity:
			p.dropPSIState(pkt.PID)
		default:
			p.dropPSIState(pkt.PID)
			feedPSI = pkt.PUSI
		}
	}
	var psiDamaged bool
	if feedPSI {
		psiDamaged = p.handlePSI(pkt)
	}
	p.slicer.Observe(pkt, verdict, p.progs, psiDamaged)
	// Note the PAT generation after slicing so the first packets of a newly
	// applied table still use the previous mapping for pre-boundary output.
	if p.patGen != p.progs.PATGeneration() {
		p.patGen = p.progs.PATGeneration()
		p.refreshPSIPIDs()
		p.progs.AdoptUnboundPMTs()
	}
}
