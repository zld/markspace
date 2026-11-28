package ingest

import (
	"encoding/binary"
	"os"
	"sort"

	"github.com/pion/rtp"

	"releasepipe/internal/clock"
	"releasepipe/internal/errs"
)

// Limits guarding against corrupt or hostile packet dumps.
const (
	MaxPackets     = 1 << 20
	MaxPacketBytes = 1 << 16
	// ReorderWindow is how many arrivals a gap may wait for its packet
	// before the loss is declared unrecoverable.
	ReorderWindow = 32
)

// RTPStats summarizes loss/recovery accounting for one packet stream.
type RTPStats struct {
	Received   int
	Duplicated int
	Lost       int // gaps never filled within ReorderWindow
	Recovered  int // gaps filled by a later arrival within ReorderWindow
}

// RecoveredPct is the percentage of lost packets that were recovered.
func (s RTPStats) RecoveredPct() float64 {
	total := s.Lost + s.Recovered
	if total == 0 {
		return 100
	}
	return 100 * float64(s.Recovered) / float64(total)
}

// PacketStream is a reassembled RTP packet flow on its own clock rate.
type PacketStream struct {
	Kind       string
	ClockRate  uint32
	Samples    []Sample // PTS in clock-rate ticks, expanded past wrap
	Stats      RTPStats
	SourcePath string
}

// ReadRTPFile reads a dump of [uint16 BE length][RTP packet] records,
// expands sequence numbers and timestamps past their modulus, reorders
// and recovers packets, and builds a sample list keyed to the clock rate.
func ReadRTPFile(path, kind string, clockRate uint32) (*PacketStream, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(errs.StageIngest, errs.CodeBadPacketStream, kind, -1, err)
	}
	if len(raw) > MaxPackets*MaxPacketBytes {
		return nil, errs.New(errs.StageIngest, errs.CodeBadPacketStream, kind, int64(MaxPacketBytes),
			"packet dump too large: %d bytes", len(raw))
	}
	type rec struct {
		pkt    *rtp.Packet
		offset int64
	}
	var records []rec
	for off := 0; off < len(raw); {
		if off+2 > len(raw) {
			return nil, errs.New(errs.StageIngest, errs.CodeBadPacketStream, kind, int64(off),
				"truncated record header")
		}
		n := int(binary.BigEndian.Uint16(raw[off:]))
		if n == 0 || n > MaxPacketBytes || off+2+n > len(raw) {
			return nil, errs.New(errs.StageIngest, errs.CodeBadPacketStream, kind, int64(off),
				"bad record length %d", n)
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw[off+2 : off+2+n]); err != nil {
			return nil, errs.New(errs.StageIngest, errs.CodeBadPacketStream, kind, int64(off),
				"RTP parse: %v", err)
		}
		records = append(records, rec{pkt: pkt, offset: int64(off)})
		off += 2 + n
		if len(records) > MaxPackets {
			return nil, errs.New(errs.StageIngest, errs.CodeBadPacketStream, kind, int64(off),
				"too many packets")
		}
	}
	ps := &PacketStream{Kind: kind, ClockRate: clockRate, SourcePath: path}
	if len(records) == 0 {
		return ps, nil
	}

	// Pass 1: expand sequence numbers in arrival order, account for
	// duplicates, losses and in-window recovery.
	var unw clock.SeqUnwrapper
	seen := map[int64]int{} // expanded seq -> arrival index
	gaps := map[int64]int{} // missing seq -> arrival index when declared
	next := int64(-1)
	for i, r := range records {
		s, err := unw.Unwrap(r.pkt.SequenceNumber)
		if err != nil {
			return nil, errs.New(errs.StageIngest, errs.CodeAmbiguousOrder, kind, r.offset,
				"sequence ordering undecidable: %v", err)
		}
		if _, dup := seen[s]; dup {
			ps.Stats.Duplicated++
			continue
		}
		seen[s] = i
		ps.Stats.Received++
		if next == -1 {
			next = s
		}
		if s >= next {
			for g := next; g < s; g++ {
				gaps[g] = i
			}
			next = s + 1
		} else if declaredAt, ok := gaps[s]; ok {
			if i-declaredAt <= ReorderWindow {
				ps.Stats.Recovered++
			} else {
				ps.Stats.Lost++
			}
			delete(gaps, s)
		}
	}
	ps.Stats.Lost += len(gaps)

	// Pass 2: sort by expanded sequence, unwrap timestamps in decode
	// order, and derive per-sample durations from timestamp deltas.
	seqs := make([]int64, 0, len(seen))
	for s := range seen {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	var tsu clock.TSUnwrapper
	tss := make([]int64, len(seqs))
	for i, s := range seqs {
		r := records[seen[s]]
		ts, err := tsu.Unwrap(r.pkt.Timestamp)
		if err != nil {
			return nil, errs.New(errs.StageIngest, errs.CodeAmbiguousOrder, kind, r.offset,
				"timestamp ordering undecidable: %v", err)
		}
		tss[i] = ts
	}
	ps.Samples = make([]Sample, len(seqs))
	for i, s := range seqs {
		r := records[seen[s]]
		dur := int64(0)
		if i+1 < len(tss) {
			dur = tss[i+1] - tss[i]
		} else if i > 0 {
			dur = tss[i] - tss[i-1]
		}
		ps.Samples[i] = Sample{
			DTS:          s,
			PTS:          tss[i],
			Duration:     dur,
			Size:         uint32(len(r.pkt.Payload)),
			SourceOffset: r.offset,
			SourceID:     path,
		}
	}
	return ps, nil
}
