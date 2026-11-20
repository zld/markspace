// Package slicer cuts a TS file into packet-count-bounded parts, injecting
// the most recent committed PAT/PMT snapshot at the head of every part and
// renumbering continuity counters per output PID.
package slicer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"tsmap/internal/demux"
	"tsmap/internal/psi"
	"tsmap/internal/ts"
)

// Options controls slicing.
type Options struct {
	OutDir     string
	MaxPackets int // input packets per slice; slices also break on table version changes
}

// FileInfo describes one written slice.
type FileInfo struct {
	Path        string `json:"path"`
	Packets     int    `json:"packets"`
	InjectedPAT int    `json:"injected_pat"`
	InjectedPMT int    `json:"injected_pmt"`
}

// Result summarizes a slice run.
type Result struct {
	Files  []FileInfo `json:"files"`
	Fatal  string     `json:"fatal,omitempty"`
}

// Slice streams input into numbered parts under Opt.OutDir.
func Slice(input string, opt Options) (*Result, error) {
	if opt.MaxPackets <= 0 {
		return nil, fmt.Errorf("max-packets must be positive")
	}
	if err := os.MkdirAll(opt.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("%s: %w", demux.FatalIO, err)
	}
	f, err := os.Open(input)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", demux.FatalIO, err)
	}
	defer f.Close()

	a := demux.New()
	r := ts.NewReader(f)
	res := &Result{}
	var cur *partWriter
	var written int
	lastSig := ""
	flush := func() error {
		if cur == nil {
			return nil
		}
		fi, err := cur.commit()
		if err != nil {
			return err
		}
		fi.Packets = written
		res.Files = append(res.Files, *fi)
		cur = nil
		written = 0
		return nil
	}
	for {
		pkt, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if cur != nil {
				cur.abort()
			}
			res.Fatal = demux.FatalResync
			return res, fmt.Errorf("%s: %w", demux.FatalResync, err)
		}
		a.Process(pkt)
		sig := versionSig(a.Tables().Current())
		if cur != nil && (written >= opt.MaxPackets || (sig != lastSig && lastSig != "")) {
			if err := flush(); err != nil {
				return res, err
			}
		}
		lastSig = sig
		if cur == nil {
			cur, err = newPartWriter(opt.OutDir, len(res.Files)+1)
			if err != nil {
				return res, err
			}
			cur.injectSnapshot(a.Tables().Current())
		}
		if err := cur.writePacket(pkt); err != nil {
			cur.abort()
			return res, fmt.Errorf("%s: %w", demux.FatalIO, err)
		}
		written++
	}
	if err := flush(); err != nil {
		return res, err
	}
	return res, nil
}

// versionSig identifies the committed table versions at a point in the stream.
func versionSig(s *psi.Snapshot) string {
	if !s.HasPAT {
		return ""
	}
	progs := make([]int, 0, len(s.Programs))
	for num := range s.Programs {
		progs = append(progs, int(num))
	}
	sort.Ints(progs)
	sig := fmt.Sprintf("pat=%d", s.PATVersion)
	for _, num := range progs {
		p := s.Programs[uint16(num)]
		if p.HasPMT {
			sig += fmt.Sprintf(",pmt%d=%d", num, p.PMTVersion)
		}
	}
	return sig
}

// partWriter accumulates one slice in a temp file inside the output
// directory and atomically renames it on commit.
type partWriter struct {
	dir   string
	final string
	tmp   *os.File
	cc    map[uint16]byte
	pat   int
	pmt   int
}

func newPartWriter(dir string, seq int) (*partWriter, error) {
	final := filepath.Join(dir, fmt.Sprintf("part-%06d.ts", seq))
	tmp, err := os.CreateTemp(dir, ".tmp-part-*.ts")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", demux.FatalIO, err)
	}
	return &partWriter{dir: dir, final: final, tmp: tmp, cc: make(map[uint16]byte)}, nil
}

// nextCC returns the current output counter for pid and advances it.
func (w *partWriter) nextCC(pid uint16) byte {
	c := w.cc[pid]
	w.cc[pid] = (c + 1) & 0xf
	return c
}

// injectSnapshot writes the committed PAT and every committed PMT at the
// head of the slice.
func (w *partWriter) injectSnapshot(s *psi.Snapshot) {
	if !s.HasPAT || len(s.RawPAT) == 0 {
		return
	}
	w.writeSection(0, s.RawPAT)
	w.pat++
	progs := make([]int, 0, len(s.Programs))
	for num := range s.Programs {
		progs = append(progs, int(num))
	}
	sort.Ints(progs)
	for _, num := range progs {
		p := s.Programs[uint16(num)]
		if p.HasPMT && len(p.RawPMT) > 0 {
			w.writeSection(p.PMTPID, p.RawPMT)
			w.pmt++
		}
	}
}

// writeSection packetizes one PSI section into 188-byte packets.
func (w *partWriter) writeSection(pid uint16, section []byte) {
	first := true
	for off := 0; off < len(section) || first; {
		var pkt [ts.PacketSize]byte
		pkt[0] = ts.SyncByte
		if first {
			pkt[1] = 0x40 // PUSI
		}
		pkt[1] |= byte(pid >> 8)
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | w.nextCC(pid) // payload only
		idx := 4
		if first {
			pkt[4] = 0x00 // pointer_field
			idx = 5
		}
		n := copy(pkt[idx:], section[off:])
		off += n
		for i := idx + n; i < ts.PacketSize; i++ {
			pkt[i] = 0xFF // stuffing
		}
		w.tmp.Write(pkt[:])
		first = false
	}
}

// writePacket appends an input packet with its continuity counter
// renumbered for the output stream.
func (w *partWriter) writePacket(p *ts.Packet) error {
	raw := p.Raw
	if p.HasPayload() {
		raw[3] = raw[3]&0xF0 | w.nextCC(p.PID)
	} else {
		raw[3] = raw[3]&0xF0 | w.cc[p.PID] // adaptation-only: counter not advanced
	}
	_, err := w.tmp.Write(raw[:])
	return err
}

func (w *partWriter) commit() (*FileInfo, error) {
	if err := w.tmp.Close(); err != nil {
		os.Remove(w.tmp.Name())
		return nil, fmt.Errorf("%s: %w", demux.FatalIO, err)
	}
	if err := os.Rename(w.tmp.Name(), w.final); err != nil {
		os.Remove(w.tmp.Name())
		return nil, fmt.Errorf("%s: %w", demux.FatalIO, err)
	}
	return &FileInfo{Path: w.final, InjectedPAT: w.pat, InjectedPMT: w.pmt}, nil
}

func (w *partWriter) abort() {
	w.tmp.Close()
	os.Remove(w.tmp.Name())
}
