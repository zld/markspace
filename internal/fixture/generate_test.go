package fixture

import (
	"bytes"
	"errors"
	"testing"

	mpierr "mediapipeline/internal/errors"
	mediartp "mediapipeline/internal/rtp"
)

func baseSpec(seed int64) Spec {
	return Spec{Seed: seed, Packets: 12, SampleRate: OpusRate, StartSeq: 100}
}

func TestDeterministicForSeed(t *testing.T) {
	a, ma, err := GenerateAudio(baseSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	b, mb, _ := GenerateAudio(baseSpec(7))
	if !bytes.Equal(a, b) {
		t.Fatal("same seed must yield byte-identical streams")
	}
	if ma.Packets != mb.Packets || len(ma.Offsets) != 12 {
		t.Fatalf("meta mismatch: %+v", ma)
	}
	d, _, _ := GenerateAudio(baseSpec(8))
	if bytes.Equal(a, d) {
		t.Fatal("different seeds must differ")
	}
}

func TestGeneratedStreamParsesCleanly(t *testing.T) {
	raw, _, err := GenerateAudio(baseSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	rd := mediartp.NewReader(bytes.NewReader(raw), OpusRate)
	n := 0
	for {
		f, err := rd.Read()
		if err != nil {
			break
		}
		if f.ExtSeq != 100+uint64(n) || f.Samples == 0 {
			t.Fatalf("frame %d wrong: %+v", n, f)
		}
		n++
	}
	if n != 12 {
		t.Fatalf("parsed %d frames, want 12", n)
	}
}

func TestCorruptionOffsetMatchesByte(t *testing.T) {
	spec := baseSpec(7)
	spec.Corrupt = map[int]int{3: mediartp.FrameHeaderSize + 2}
	raw, meta, err := GenerateAudio(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := meta.CorruptOffsets[3]
	if want <= 0 {
		t.Fatal("corrupt offset not recorded")
	}
	// Independently verify the parser reports the exact same byte.
	bad := bytes.Clone(raw)
	bad[want] ^= 0xAA
	rd := mediartp.NewReader(bytes.NewReader(bad), OpusRate)
	for i := 0; i < 12; i++ {
		_, err := rd.Read()
		if err == nil {
			continue
		}
		var me *mpierr.MediaError
		if !errors.As(err, &me) {
			t.Fatalf("unexpected non-media error: %v", err)
		}
		if me.ByteOffset != want {
			t.Fatalf("parser offset %d != injected %d", me.ByteOffset, want)
		}
		return
	}
	t.Fatal("corrupted packet never produced an error")
}

func TestRolloverAtSequenceBoundary(t *testing.T) {
	spec := baseSpec(7)
	spec.StartSeq = 65532
	spec.Packets = 8 // crosses 65535 -> 0
	raw, _, err := GenerateAudio(spec)
	if err != nil {
		t.Fatal(err)
	}
	rd := mediartp.NewReader(bytes.NewReader(raw), OpusRate)
	var ext []uint64
	for {
		f, err := rd.Read()
		if err != nil {
			break
		}
		ext = append(ext, f.ExtSeq)
	}
	want := []uint64{65532, 65533, 65534, 65535, 65536, 65537, 65538, 65539}
	if len(ext) != len(want) {
		t.Fatalf("ext = %v, want %v", ext, want)
	}
	for i := range want {
		if ext[i] != want[i] {
			t.Fatalf("rollover ext = %v, want %v", ext, want)
		}
	}
}

func TestDropAndDuplicate(t *testing.T) {
	spec := baseSpec(7)
	spec.Drop = map[int]bool{2: true}
	spec.Duplicate = map[int]bool{5: true}
	raw, meta, err := GenerateAudio(spec)
	if err != nil {
		t.Fatal(err)
	}
	rd := mediartp.NewReader(bytes.NewReader(raw), OpusRate)
	seqs := map[uint64]int{}
	for {
		f, err := rd.Read()
		if err != nil {
			break
		}
		seqs[f.ExtSeq]++
	}
	if seqs[102] != 0 {
		t.Fatal("dropped packet 2 must be absent")
	}
	if seqs[105] != 2 {
		t.Fatalf("duplicate count for seq 105 = %d, want 2", seqs[105])
	}
	if meta.Duplicates != 1 || len(meta.Dropped) != 1 {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestLateAndTruncate(t *testing.T) {
	spec := baseSpec(7)
	spec.Late = map[int]int{1: 9} // moved toward the tail
	raw, meta, err := GenerateAudio(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.LatePackets) != 1 {
		t.Fatalf("late meta = %+v", meta.LatePackets)
	}
	short, _, err := GenerateAudio(func() Spec {
		s := baseSpec(7)
		s.TruncateAt = 5
		return s
	}())
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 5 || len(raw) <= 5 {
		t.Fatalf("truncation wrong: short=%d full=%d", len(short), len(raw))
	}
}

func TestVideoStream(t *testing.T) {
	raw, meta, err := GenerateVideo(7, 6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Frames != 6 || meta.Timestamps[1] != VideoStep {
		t.Fatalf("video meta = %+v", meta)
	}
	rd := mediartp.NewReader(bytes.NewReader(raw), VideoRate)
	n := 0
	for {
		if _, err := rd.Read(); err != nil {
			break
		}
		n++
	}
	if n != 6 {
		t.Fatalf("video frames parsed = %d, want 6", n)
	}
}

func TestRejectsOutOfRange(t *testing.T) {
	if _, _, err := GenerateAudio(Spec{Seed: 1, Packets: 0}); err == nil {
		t.Fatal("zero packets must be rejected")
	}
	if _, _, err := GenerateAudio(Spec{Seed: 1, Packets: mediartp.MaxPackets + 1}); err == nil {
		t.Fatal("oversized fixture must be rejected")
	}
}
