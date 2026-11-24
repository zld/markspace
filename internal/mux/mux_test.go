package mux

import (
	"bytes"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fixture"
	"mediapack/internal/track"
)

func packets(t *testing.T) ([]*demux.Packet, *track.Mapping, *fixture.Detail) {
	t.Helper()
	spec := fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS"},
		Clusters: 3, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
	raw, detail := fixture.Build(spec, fixture.CorruptNone)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var packets []*demux.Packet
	if err := d.Stream(func(p *demux.Packet) error {
		packets = append(packets, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return packets, d.Mapping(), &detail
}

func TestWebMLosslessRoundTrip(t *testing.T) {
	pkts, mapping, detail := packets(t)
	var out bytes.Buffer
	res, err := WriteWebM(&out, mapping, pkts)
	if err != nil {
		t.Fatal(err)
	}
	if res.BlockCount != len(pkts) || res.BlockCount != detail.BlockCount {
		t.Fatalf("block count=%d packets=%d want %d",
			res.BlockCount, len(pkts), detail.BlockCount)
	}
	// Every per-block digest must equal the corresponding input payload hash.
	for i, p := range pkts {
		want := hashPayload(p.Payload)
		if res.Digests[i].SHA256 != want {
			t.Fatalf("block %d payload hash changed during remux", i)
		}
	}
	// The remuxed WebM must parse again and yield byte-identical payloads.
	d2, err := demux.Open(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var again []*demux.Packet
	if err := d2.Stream(func(p *demux.Packet) error {
		again = append(again, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(again) != len(pkts) {
		t.Fatalf("re-demux count=%d want %d", len(again), len(pkts))
	}
	for i := range pkts {
		if !bytes.Equal(again[i].Payload, pkts[i].Payload) {
			t.Fatalf("payload %d not byte-identical after round trip", i)
		}
	}
}

func TestWebMDeterministicOutput(t *testing.T) {
	pkts, mapping, _ := packets(t)
	m := mapping
	run := func() []byte {
		var b bytes.Buffer
		if _, err := WriteWebM(&b, m, pkts); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	if !bytes.Equal(run(), run()) {
		t.Fatal("same packets produced different WebM bytes across runs")
	}
}

func TestMP4StructuralValidation(t *testing.T) {
	pkts, mapping, _ := packets(t)
	var out bytes.Buffer
	ws := &writeSeeker{Buffer: &out}
	res, err := WriteMP4(ws, mapping, pkts)
	if err != nil {
		t.Fatal(err)
	}
	if res.BlockCount != len(pkts) {
		t.Fatalf("mp4 block count=%d want %d", res.BlockCount, len(pkts))
	}
	// Reconstruct the expected mdat payload (verbatim packet concatenation).
	var wantMDAT bytes.Buffer
	for _, p := range pkts {
		wantMDAT.Write(p.Payload)
	}
	if err := VerifyRoundTrip(bytes.NewReader(out.Bytes()), wantMDAT.Bytes()); err != nil {
		t.Fatalf("mp4 structural verification failed: %v", err)
	}
	// Per-block hashes still match the input payloads.
	for i, p := range pkts {
		if res.Digests[i].SHA256 != hashPayload(p.Payload) {
			t.Fatalf("mp4 block %d payload hash changed", i)
		}
	}
}

func TestPayloadHashOrderSensitive(t *testing.T) {
	pkts, mapping, _ := packets(t)
	var a bytes.Buffer
	resA, err := WriteWebM(&a, mapping, pkts)
	if err != nil {
		t.Fatal(err)
	}
	// Swap two packets and confirm the aggregate hash changes.
	swapped := make([]*demux.Packet, len(pkts))
	copy(swapped, pkts)
	swapped[0], swapped[1] = swapped[1], swapped[0]
	var b bytes.Buffer
	resB, err := WriteWebM(&b, mapping, swapped)
	if err != nil {
		t.Fatal(err)
	}
	if resA.PayloadHash == resB.PayloadHash {
		t.Fatal("aggregate payload hash must be order-sensitive")
	}
}

// writeSeeker adapts bytes.Buffer to io.WriteSeeker for MP4 generation.
type writeSeeker struct {
	*bytes.Buffer
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	// go-mp4 seeks forward/back to write headers after sizes are known; the
	// in-memory implementation grows the buffer when seeking past the end.
	end := w.pos + int64(len(p))
	for int64(w.Len()) < end {
		w.Buffer.WriteByte(0)
	}
	buf := w.Buffer.Bytes()
	copy(buf[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		w.pos = offset
	case 1:
		w.pos += offset
	case 2:
		w.pos = int64(w.Len()) + offset
	}
	return w.pos, nil
}
