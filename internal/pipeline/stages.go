package pipeline

import (
	"encoding/binary"
	"os"

	"mediacore/internal/bitstream"
	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/rtp"
)

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fault.Newf(fault.CodeInternal, "", "cannot stat %s: %v", path, err)
	}
	return fi.Size(), nil
}

// buildIndex lists every scanned frame so the index exercises both keyframe
// and non-keyframe validation. Timestamps use a 90 kHz video clock.
func buildIndex(s *bitstream.Stream) *bitstream.Index {
	idx := &bitstream.Index{Version: bitstream.IndexVersion, Codec: s.Codec, Timescale: 90000, Entries: make([]bitstream.IndexEntry, 0, len(s.Frames))}
	for _, f := range s.Frames {
		idx.Entries = append(idx.Entries, bitstream.IndexEntry{
			Offset: f.Offset, Size: f.Size,
			Timestamp: int64(f.Index) * 90000 / 30,
			Keyframe:  f.Keyframe,
		})
	}
	return idx
}

// corruptIndex plants the documented index faults: an entry pointing inside
// a frame, a non-keyframe row mislabelled as a keyframe, and an out-of-range
// row. All three are quarantined and the run is rejected.
func corruptIndex(idx *bitstream.Index) {
	if len(idx.Entries) > 2 {
		idx.Entries[2].Offset += 7
	}
	for i := 1; i < len(idx.Entries); i++ {
		if !idx.Entries[i].Keyframe {
			idx.Entries[i].Keyframe = true
			break
		}
	}
	idx.Entries = append(idx.Entries, bitstream.IndexEntry{Offset: int64(1) << 40, Size: 4, Timestamp: 1 << 40})
}

// packetizeStream splits the stream into RTP packets, drops and reorders
// deterministically, and duplicates a couple of packets to exercise
// idempotency.
func packetizeStream(raw []byte, stream *bitstream.Stream, opts Options) ([][]byte, []uint16, error) {
	const payloadSize = 1200
	var packets [][]byte
	seq := uint16(opts.Seed % 30000)
	for i, f := range stream.Frames {
		marker := i == len(stream.Frames)-1
		pkts, err := rtp.Packetize(raw[f.Offset:f.Offset+f.Size], payloadSize, seq, uint32(f.Index)*3000, 0xA11CE, f.Keyframe, marker)
		if err != nil {
			return nil, nil, err
		}
		seq += uint16(len(pkts))
		packets = append(packets, pkts...)
	}
	if opts.Loss < 0 || opts.Loss >= len(packets) {
		return nil, nil, fault.Newf(fault.CodeRTPSequenceLimit, "", "loss count %d does not fit %d packets", opts.Loss, len(packets))
	}
	// Delayed packets are reinserted a few positions later so the jitter
	// buffer can recover them inside the wait window. This models jitter and
	// a retransmit-free recovery path rather than permanent loss.
	held := make(map[int]int)
	stride := len(packets) / (opts.Loss + 1)
	if opts.Loss > 0 && stride > 0 {
		for i := 1; i <= opts.Loss; i++ {
			held[i*stride] = opts.WaitWindow / 2
		}
	}
	queue := make(map[int][][]byte)
	var delayed []uint16
	var survivors [][]byte
	for i, p := range packets {
		if at, ok := held[i]; ok {
			target := i + at
			if target < i+1 {
				target = i + 1
			}
			queue[target] = append(queue[target], p)
			delayed = append(delayed, binary.BigEndian.Uint16(p[2:4]))
			continue
		}
		survivors = append(survivors, p)
		if q, ok := queue[i]; ok {
			survivors = append(survivors, q...)
			delete(queue, i)
		}
	}
	for _, pos := range sortedIntKeys(queue) {
		survivors = append(survivors, queue[pos]...)
	}
	for n := 0; n < opts.Reorder; n++ {
		j := 3 + n*21
		if j+1 < len(survivors) {
			survivors[j], survivors[j+1] = survivors[j+1], survivors[j]
		}
	}
	if len(survivors) > 10 {
		survivors = append(survivors, survivors[5])
		survivors = append(survivors, survivors[len(survivors)-3])
	}
	return survivors, delayed, nil
}

// countDelivered reports how many of the delayed sequence numbers reached the
// recovered output.
func countDelivered(rec *rtp.Recovery, delayed []uint16) int {
	if len(delayed) == 0 {
		return 0
	}
	want := make(map[uint16]bool, len(delayed))
	for _, s := range delayed {
		want[s] = true
	}
	seen := 0
	for _, seg := range rec.Segments {
		for _, p := range seg.Packets {
			if want[p.Seq] {
				delete(want, p.Seq)
				seen++
			}
		}
	}
	return seen
}

// recoverPackets feeds the lossy stream through the recoverer.
func recoverPackets(packets [][]byte, opts Options) (*rtp.Recovery, error) {
	cfg := rtp.DefaultConfig()
	cfg.WaitWindow = opts.WaitWindow
	cfg.BufferLimit = opts.BufferLimit
	rec, err := rtp.NewRecoverer(cfg)
	if err != nil {
		return nil, err
	}
	for i, raw := range packets {
		if _, err := rec.Push(raw, clock.Time(int64(i))*clock.Millisecond/2); err != nil {
			return nil, err
		}
	}
	return rec.Flush()
}
