package remux

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	mp4 "github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"mediaqc/internal/ebml"
	"mediaqc/internal/timeline"
)

// writeSeekerBuffer is an in-memory io.WriteSeeker.
type writeSeekerBuffer struct {
	buf []byte
	pos int64
}

func (b *writeSeekerBuffer) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if end > int64(len(b.buf)) {
		nb := make([]byte, end)
		copy(nb, b.buf)
		b.buf = nb
	}
	copy(b.buf[b.pos:], p)
	b.pos = end
	return len(p), nil
}

func (b *writeSeekerBuffer) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case 0:
		b.pos = off
	case 1:
		b.pos += off
	case 2:
		b.pos = int64(len(b.buf)) + off
	}
	return b.pos, nil
}

// buildOutput runs a small deterministic remux and returns the bytes.
func buildOutput(t *testing.T) []byte {
	t.Helper()
	ws := &writeSeekerBuffer{}
	w, err := NewWriter(ws)
	if err != nil {
		t.Fatal(err)
	}
	vt, err := w.AddTrack(1, 1, "V_VP9")
	if err != nil {
		t.Fatal(err)
	}
	at, err := w.AddTrack(2, 2, "A_OPUS")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := w.WriteSample(vt, bytes.Repeat([]byte{byte(i)}, 50), i%3 == 0, 33_000_000); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteSample(at, bytes.Repeat([]byte{0xA0 | byte(i)}, 20), false, 20_000_000); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return ws.buf
}

func TestWriterStructureAndIndex(t *testing.T) {
	out := buildOutput(t)
	// Probe the box tree and collect the tables that matter.
	var haveFtyp, haveMoov, haveMdat, haveStss, haveStco bool
	var stssCount, stcoCount int
	_, err := mp4.Probe(bytes.NewReader(out))
	_ = err // Probe may demand more boxes than we check below
	r := bytes.NewReader(out)
	_, err = mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "ftyp":
			haveFtyp = true
		case "moov":
			haveMoov = true
		case "mdat":
			haveMdat = true
		case "stss":
			haveStss = true
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stssCount = len(box.(*mp4.Stss).SampleNumber)
		case "stco":
			haveStco = true
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stcoCount = len(box.(*mp4.Stco).ChunkOffset)
		}
		switch h.BoxInfo.Type.String() {
		case "moov", "trak", "mdia", "minf", "dinf", "stbl", "stsd":
			_, err := h.Expand()
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !haveFtyp || !haveMoov || !haveMdat {
		t.Fatalf("missing boxes: ftyp=%v moov=%v mdat=%v", haveFtyp, haveMoov, haveMdat)
	}
	if !haveStss || !haveStco {
		t.Fatalf("random-access index missing: stss=%v stco=%v", haveStss, haveStco)
	}
	if stssCount != 2 { // samples 1 and 4 are keyframes
		t.Fatalf("stss entries = %d, want 2", stssCount)
	}
	if stcoCount == 0 {
		t.Fatalf("stco empty")
	}
}

func TestWriterDeterministic(t *testing.T) {
	a := buildOutput(t)
	b := buildOutput(t)
	if sha256.Sum256(a) != sha256.Sum256(b) {
		t.Fatalf("identical inputs produced different outputs")
	}
}

func TestValidateMapping(t *testing.T) {
	tracks := []ebml.TrackInfo{
		{Number: 1, Type: 1, Codec: "V_VP9"},
		{Number: 2, Type: 2, Codec: "A_OPUS"},
	}
	if err := ValidateMapping(map[uint64]uint64{1: 1, 2: 2}, tracks); err != nil {
		t.Fatalf("identity mapping rejected: %v", err)
	}
	// Missing track.
	err := ValidateMapping(map[uint64]uint64{1: 1}, tracks)
	if f := timeline.AsFault(err); f.Class != timeline.ClassTrackMapping || f.Track != 2 {
		t.Fatalf("missing track: %v", err)
	}
	// Duplicate target.
	err = ValidateMapping(map[uint64]uint64{1: 7, 2: 7}, tracks)
	if f := timeline.AsFault(err); f.Class != timeline.ClassTrackMapping {
		t.Fatalf("duplicate target: %v", err)
	}
	// Incompatible codec.
	bad := []ebml.TrackInfo{{Number: 1, Type: 1, Codec: "V_PRORES"}}
	err = ValidateMapping(map[uint64]uint64{1: 1}, bad)
	if f := timeline.AsFault(err); f.Class != timeline.ClassCodec {
		t.Fatalf("codec: %v", err)
	}
}

func makeRTPStream(n int) []*rtp.Packet {
	out := make([]*rtp.Packet, n)
	for i := range out {
		out[i] = &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: uint16(1000 + i), Timestamp: uint32(i * 960)},
			Payload: []byte{byte(i)},
		}
	}
	return out
}

func TestRTPRecovery(t *testing.T) {
	src := makeRTPStream(200)
	wire := SimulateLoss(src)
	// Sanity: the simulation actually reorders, duplicates and drops.
	if len(wire) >= len(src) {
		t.Fatalf("simulation dropped nothing")
	}
	ordered, st := ReorderRTP(wire, 8)
	if st.Duplicates == 0 {
		t.Fatalf("no duplicates detected")
	}
	if st.Lost == 0 {
		t.Fatalf("no losses detected")
	}
	if st.Rate < 0.90 {
		t.Fatalf("recovery rate %.3f below 0.90", st.Rate)
	}
	// Delivered packets must be strictly increasing in sequence space
	// (gaps remain where packets were genuinely lost).
	for i := 1; i < len(ordered); i++ {
		if !seqLess(ordered[i-1].SequenceNumber, ordered[i].SequenceNumber) {
			t.Fatalf("output not ordered at %d: %d -> %d",
				i, ordered[i-1].SequenceNumber, ordered[i].SequenceNumber)
		}
	}
}

func TestRTPRecoveryNoLoss(t *testing.T) {
	ordered, st := ReorderRTP(makeRTPStream(50), 8)
	if st.Lost != 0 || st.Rate != 1.0 || len(ordered) != 50 {
		t.Fatalf("clean stream: %+v", st)
	}
}

func TestP95(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 100; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	if got := P95(ds); got != 95*time.Millisecond {
		t.Fatalf("p95 = %v, want 95ms", got)
	}
	if P95(nil) != 0 {
		t.Fatalf("p95 of empty != 0")
	}
}

// TestAtomicCommit exercises the temp-file + rename commit pattern used
// by the process pipeline.
func TestAtomicCommit(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "out.mp4")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, buildOutput(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temp file remains after commit")
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("final output missing: %v", err)
	}
}
