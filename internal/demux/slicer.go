package demux

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"tsinspect/internal/ts"
)

// SliceError reports a slicing precondition that could not be met.
type SliceError struct{ Err error }

func (e *SliceError) Error() string { return e.Err.Error() }
func (e *SliceError) Unwrap() error { return e.Err }

// ErrProgramNotFound is returned when the rebuilt PAT has no such program.
var ErrProgramNotFound = errors.New("program not found in rebuilt PAT")

// ErrPMTPending is returned when the program exists but its PMT never became
// complete and CRC-valid.
var ErrPMTPending = errors.New("PMT is not complete for program")

// SliceRequest describes one slicing job.
type SliceRequest struct {
	InputPath  string
	OutputPath string
	Program    uint16
	Offset     int64
}

// SliceResult reports what was written.
type SliceResult struct {
	Packets    int64
	Bytes      int64
	PMTVersion int
	Streams    []uint16
}

// pktWriter writes selected packets with per-PID continuity counters
// renumbered to a clean sequence. Adaptation-only packets keep the counter of
// the preceding payload packet, as required by ISO/IEC 13818-1.
type pktWriter struct {
	w       io.Writer
	cc      map[uint16]uint8
	packets int64
	bytes   int64
}

func newPktWriter(w io.Writer) *pktWriter {
	return &pktWriter{w: w, cc: make(map[uint16]uint8)}
}

func (pw *pktWriter) write(raw []byte) error {
	if len(raw) != ts.PacketSize {
		return fmt.Errorf("slice: refusing to write a %d-byte packet", len(raw))
	}
	b := make([]byte, ts.PacketSize)
	copy(b, raw)
	pid := uint16(b[1]&0x1f)<<8 | uint16(b[2])
	hasPayload := b[3]&0x10 != 0
	hasAF := b[3]&0x20 != 0
	if hasPayload {
		next := pw.cc[pid]
		pw.cc[pid] = (next + 1) & 0x0f
		b[3] = b[3]&0xf0 | next
	} else if hasAF {
		b[3] = b[3]&0xf0 | pw.cc[pid]
	}
	if _, err := pw.w.Write(b); err != nil {
		return fmt.Errorf("slice: writing packet: %w", err)
	}
	pw.packets++
	pw.bytes += int64(len(b))
	return nil
}

// Slice performs a full streaming pass, rebuilding tables exactly as inspect
// does. It only writes once the program's PMT has become complete and
// CRC-valid, and a second pass re-feeds the same PSI parser to confirm the
// mapping is still effective at the packets being emitted. Output goes to a
// temporary file in the destination directory and is renamed on success, so a
// failed slice never leaves a valid output behind.
func Slice(req SliceRequest) (*SliceResult, error) {
	if req.InputPath == "" || req.OutputPath == "" {
		return nil, fmt.Errorf("slice: input and output paths are required")
	}
	if req.Offset < 0 {
		return nil, fmt.Errorf("slice: offset must be non-negative")
	}
	inInfo, err := os.Stat(req.InputPath)
	if err != nil {
		return nil, fmt.Errorf("slice: %w", err)
	}
	if outInfo, statErr := os.Stat(req.OutputPath); statErr == nil {
		if os.SameFile(inInfo, outInfo) || samePath(req.InputPath, req.OutputPath) {
			return nil, &SliceError{Err: fmt.Errorf("refusing to overwrite input %s", req.InputPath)}
		}
	} else if samePath(req.InputPath, req.OutputPath) {
		return nil, &SliceError{Err: fmt.Errorf("refusing to overwrite input %s", req.InputPath)}
	}

	// Pass 1: rebuild tables from the whole stream.
	probe := NewSession()
	r, err := ts.Open(req.InputPath, req.Offset)
	if err != nil {
		return nil, fmt.Errorf("slice: %w", err)
	}
	for {
		p, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			r.Close()
			return nil, fmt.Errorf("slice: %w", err)
		}
		if err := probe.Feed(p); err != nil {
			r.Close()
			return nil, fmt.Errorf("slice: %w", err)
		}
	}
	r.Close()

	pmt, ok := probe.Tables.PMT(req.Program)
	if !ok {
		if _, mapped := probe.Tables.ProgramPMTPID(req.Program); !mapped {
			return nil, &SliceError{Err: fmt.Errorf("%w: %d", ErrProgramNotFound, req.Program)}
		}
		return nil, &SliceError{Err: fmt.Errorf("%w %d", ErrPMTPending, req.Program)}
	}
	pmtPID, ok := probe.Tables.ProgramPMTPID(req.Program)
	if !ok {
		return nil, &SliceError{Err: fmt.Errorf("%w: %d", ErrProgramNotFound, req.Program)}
	}
	keep := map[uint16]bool{PATPID: true, pmtPID: true}
	mediaPIDs := make(map[uint16]bool)
	for _, es := range pmt.Streams {
		keep[es.PID] = true
		mediaPIDs[es.PID] = true
	}

	// Pass 2: confirm boundaries with a fresh session and write the slice.
	info, err := writeSlice(req, keep, mediaPIDs, pmt.Version)
	if err != nil {
		return nil, err
	}
	res := &SliceResult{
		PMTVersion: int(pmt.Version),
		Packets:    info.packets,
		Bytes:      info.bytes,
	}
	for _, es := range pmt.Streams {
		res.Streams = append(res.Streams, es.PID)
	}
	return res, nil
}

type writeStats struct {
	packets int64
	bytes   int64
}

func writeSlice(req SliceRequest, keep, mediaPIDs map[uint16]bool, version uint8) (stats writeStats, err error) {
	dir := filepath.Dir(req.OutputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return stats, fmt.Errorf("slice: creating output directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tsinspect-slice-*")
	if err != nil {
		return stats, fmt.Errorf("slice: creating temporary output: %w", err)
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		if !published {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	r, err := ts.Open(req.InputPath, req.Offset)
	if err != nil {
		return stats, fmt.Errorf("slice: %w", err)
	}
	defer r.Close()

	// The confirmation session is the same parser used by inspect. Media
	// packets are only emitted while it reports the expected PMT version.
	confirm := NewSession()
	pw := newPktWriter(tmp)
	for {
		p, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return stats, fmt.Errorf("slice: %w", err)
		}
		if err := confirm.Feed(p); err != nil {
			return stats, fmt.Errorf("slice: %w", err)
		}
		// PSI packets are always eligible so the slice is self-describing.
		// Media packets are only emitted while the confirming parser reports
		// the program's PMT as active at the expected version.
		if !keep[p.PID] {
			continue
		}
		if mediaPIDs[p.PID] {
			pmt, ok := confirm.Tables.PMT(req.Program)
			if !ok || pmt.Version != version {
				continue
			}
		}
		if err := pw.write(p.Raw); err != nil {
			return stats, err
		}
	}
	if err := tmp.Sync(); err != nil {
		return stats, fmt.Errorf("slice: syncing output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return stats, fmt.Errorf("slice: closing output: %w", err)
	}
	if pw.packets == 0 {
		return stats, &SliceError{Err: fmt.Errorf("program %d produced no packets", req.Program)}
	}
	// CreateTemp gives 0600; widen to the usual 0644 for a media artifact.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return stats, fmt.Errorf("slice: setting output mode: %w", err)
	}
	if err := os.Rename(tmpName, req.OutputPath); err != nil {
		return stats, fmt.Errorf("slice: publishing output: %w", err)
	}
	published = true
	return writeStats{packets: pw.packets, bytes: pw.bytes}, nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if aa == bb {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
