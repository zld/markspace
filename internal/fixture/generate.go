// Package fixture self-generates normal and corrupted media samples.
// Everything is deterministic from the seed and stays inside the work
// directory; no external media or network input is ever used.
package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// Options controls fixture generation.
type Options struct {
	Seed      int64
	TargetBps int64
	Duration  int64 // seconds of media to synthesize (>=1)
	FaultSet  string
}

// Set lists the generated files.
type Set struct {
	NormalMP4 string
	TruncMP4  string
	OobMP4    string
	RTPNormal string
	RTPFault  string
	Faults    []string
}

const (
	videoTimescale = 90000
	audioTimescale = 48000
	videoDelta     = 3000 // 30 fps
	audioDelta     = 1024
)

// Generate writes all fixture files into dir and returns their paths.
func Generate(dir string, opts Options) (*Set, error) {
	if opts.Duration < 1 || opts.Duration > 60 {
		return nil, fmt.Errorf("fixture: duration %d out of range [1,60]", opts.Duration)
	}
	if opts.TargetBps < 1000 || opts.TargetBps > 1<<30 {
		return nil, fmt.Errorf("fixture: target bitrate %d out of range", opts.TargetBps)
	}
	rng := rand.New(rand.NewSource(opts.Seed))
	s := &Set{}
	var err error

	normal := buildMP4(rng, opts, false)
	if s.NormalMP4, err = write(dir, "normal.mp4", normal); err != nil {
		return nil, err
	}
	// Fault 1: truncated box (cut inside mdat payload).
	trunc := normal[:len(normal)-len(normal)/3]
	if s.TruncMP4, err = write(dir, "fault_truncated.mp4", trunc); err != nil {
		return nil, err
	}
	// Fault 2: sample table offset beyond file boundary.
	if s.OobMP4, err = write(dir, "fault_oob_samples.mp4", buildMP4(rng, opts, true)); err != nil {
		return nil, err
	}
	// RTP streams: normal and fault (duplicates, wrap, burst loss, regression).
	if s.RTPNormal, err = write(dir, "stream_normal.rtp", buildRTP(rng, opts, false)); err != nil {
		return nil, err
	}
	if s.RTPFault, err = write(dir, "stream_fault.rtp", buildRTP(rng, opts, true)); err != nil {
		return nil, err
	}
	s.Faults = []string{"truncated-box", "oob-sample-table", "rtp-dup-wrap", "timestamp-regression", "manifest-corruption"}
	return s, nil
}

func write(dir, name string, data []byte) (string, error) {
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, data, 0o644)
}

// --- MP4 synthesis ---

type track struct {
	id        uint32
	timescale uint32
	handler   string
	delta     uint32
	sizes     []uint32
}

func boxOf(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func fullbox(typ string, payload []byte) []byte {
	return boxOf(typ, append([]byte{0, 0, 0, 0}, payload...))
}

func u32(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.BigEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// sampleSizes distributes totalBytes across n samples with deterministic
// jitter of at most ±2% so the actual bitrate stays well within 5%.
func sampleSizes(rng *rand.Rand, n int, totalBytes int64) []uint32 {
	sizes := make([]uint32, n)
	base := totalBytes / int64(n)
	for i := range sizes {
		jitter := int64(rng.Intn(5)) - 2 // -2..+2 percent
		sizes[i] = uint32(base * (100 + jitter) / 100)
	}
	return sizes
}

func buildMP4(rng *rand.Rand, opts Options, oob bool) []byte {
	dur := opts.Duration
	total := opts.TargetBps * dur / 8
	nV := int(30 * dur)
	nA := int(48000 * dur / audioDelta)
	vBytes := total * 9 / 10
	aBytes := total - vBytes
	tracks := []track{
		{id: 1, timescale: videoTimescale, handler: "vide", delta: videoDelta,
			sizes: sampleSizes(rng, nV, vBytes)},
		{id: 2, timescale: audioTimescale, handler: "soun", delta: audioDelta,
			sizes: sampleSizes(rng, nA, aBytes)},
	}
	build := func(offsets []uint32) []byte {
		var traks []byte
		for i, t := range tracks {
			stts := fullbox("stts", append(u32(1), u32(uint32(len(t.sizes)), t.delta)...))
			stsc := fullbox("stsc", append(u32(1), u32(1, uint32(len(t.sizes)), 1)...))
			stsz := fullbox("stsz", append(u32(0, uint32(len(t.sizes))), u32(t.sizes...)...))
			stco := fullbox("stco", append(u32(1), u32(offsets[i])...))
			mdia := boxOf("mdia", cat(
				fullbox("mdhd", append(u32(0, 0, t.timescale, t.delta*uint32(len(t.sizes))), make([]byte, 4)...)),
				fullbox("hdlr", cat(u32(0), []byte(t.handler), make([]byte, 13))),
				boxOf("minf", boxOf("stbl", cat(stts, stsc, stsz, stco))),
			))
			tkhd := fullbox("tkhd", cat(u32(0, 0, t.id, 0, 0), make([]byte, 8+8+36+8)))
			traks = cat(traks, boxOf("trak", cat(tkhd, mdia)))
		}
		return boxOf("moov", traks)
	}
	ftyp := boxOf("ftyp", cat([]byte("isom"), u32(0)))
	moov := build([]uint32{0, 0})
	mdatOff := uint32(len(ftyp) + len(moov) + 8)
	if oob {
		mdatOff += 1 << 22 // sample offsets land beyond EOF
	}
	moov = build([]uint32{mdatOff, mdatOff})
	var payload []byte
	for _, t := range tracks {
		for _, sz := range t.sizes {
			payload = append(payload, make([]byte, sz)...)
		}
	}
	rng.Read(payload[:min(len(payload), 1<<16)]) // deterministic content prefix
	return cat(ftyp, moov, boxOf("mdat", payload))
}

// --- RTP synthesis ---

// buildRTP writes length-prefixed RTP datagrams. Fault mode starts the
// sequence near wrap-around, injects duplicates, a burst loss and a
// timestamp regression.
func buildRTP(rng *rand.Rand, opts Options, fault bool) []byte {
	n := int(opts.TargetBps * opts.Duration / 8 / 1200)
	if n < 32 {
		n = 32
	}
	var seq uint16 = 100
	if fault {
		seq = 65530 // exercise wrap-around
	}
	var out []byte
	ts := uint32(90000)
	emit := func(seq uint16, ts uint32) {
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xfeed},
			Payload: make([]byte, 1200),
		}
		rng.Read(p.Payload[:64])
		raw, _ := p.Marshal()
		var lb [4]byte
		binary.LittleEndian.PutUint32(lb[:], uint32(len(raw)))
		out = append(out, lb[:]...)
		out = append(out, raw...)
	}
	var heldSeq uint16
	var heldTS uint32
	holding := false
	for i := 0; i < n; i++ {
		if fault && i == n/2 {
			seq += 6 // burst loss of 6 packets
		}
		cur := ts
		if fault && i == 3*n/4 {
			cur -= 90000 * 10 // timestamp regression: 10 s backward
		}
		if fault && i%11 == 5 {
			heldSeq, heldTS, holding = seq, cur, true // reorder: emit one packet late
		} else {
			emit(seq, cur)
			if holding {
				emit(heldSeq, heldTS) // arrives after a higher seq: recoverable
				holding = false
			}
		}
		if fault && i%7 == 0 {
			emit(seq, cur) // duplicate
		}
		seq++
		ts += 3000
	}
	if holding {
		emit(heldSeq, heldTS)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
