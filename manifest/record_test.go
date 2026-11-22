package manifest

import (
	"bytes"
	"io"
	"testing"

	"medpipe/timeline"
)

// memSeeker is an in-memory io.WriteSeeker supporting the back-seeks go-mp4
// performs when finalizing box headers.
type memSeeker struct {
	data []byte
	pos  int64
}

func newMemSeeker(_ *bytes.Buffer) *memSeeker { return &memSeeker{} }

func (m *memSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if int64(len(m.data)) < end {
		grown := make([]byte, end)
		copy(grown, m.data) // preserve existing bytes across growth
		m.data = grown
	}
	copy(m.data[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = offset
	case io.SeekCurrent:
		m.pos += offset
	case io.SeekEnd:
		m.pos = int64(len(m.data)) + offset
	}
	if m.pos < 0 {
		return 0, io.EOF
	}
	return m.pos, nil
}

// Bytes returns the serialized carrier.
func (m *memSeeker) Bytes() []byte { return append([]byte(nil), m.data...) }

func goodRecord(id string) TrackRecord {
	clock, _ := timeline.NewClock(48000)
	frames := int64(48000) // exactly one second
	return TrackRecord{
		TrackID: id, InputRate: 48000, OutputRate: 48000,
		InputFrames: frames, ResampledFrames: frames, OutputFrames: frames,
		StartSample: 0, EndSample: frames,
		DurationNanos: clock.SamplesToNanos(frames),
		InputLUFS: -30, OutputLUFS: -23, TargetLUFS: -23,
		TruePeakDBTP: -1.5, ClippedSamples: 12,
		TargetBitrateBPS: 768000, ActualBitrateBPS: 770000,
		BitrateDeltaPct: 0.26, AlignmentMillis: 0,
	}
}

func TestBuilderAcceptsAlignedRejectsMismatch(t *testing.T) {
	clock, _ := timeline.NewClock(48000)
	b := NewBuilder(clock, 17, 48000, 2, 4)
	r := goodRecord("t1")
	if err := b.AddTrack(r); err != nil {
		t.Fatalf("aligned record rejected: %v", err)
	}
	bad := goodRecord("t2")
	bad.EndSample = bad.StartSample + bad.OutputFrames + 5
	if err := b.AddTrack(bad); err == nil {
		t.Fatal("interval/frame mismatch must be rejected")
	}
	bad = goodRecord("t2")
	bad.ResampledFrames = bad.OutputFrames - 1
	if err := b.AddTrack(bad); err == nil {
		t.Fatal("resampled/output frame mismatch must be rejected")
	}
	bad = goodRecord("t2")
	bad.DurationNanos += 5_000_000
	if err := b.AddTrack(bad); err == nil {
		t.Fatal("duration mismatch must be rejected")
	}
	bad = goodRecord("t2")
	bad.BitrateDeltaPct = 2.5
	if err := b.AddTrack(bad); err == nil {
		t.Fatal("bitrate delta beyond 2% must be rejected")
	}
	bad = goodRecord("t2")
	bad.TruePeakDBTP = -0.5
	if err := b.AddTrack(bad); err == nil {
		t.Fatal("true peak above -1 dBTP must be rejected")
	}
}

func TestHashDeterministicAcrossBuilders(t *testing.T) {
	clock, _ := timeline.NewClock(48000)
	build := func() Manifest {
		b := NewBuilder(clock, 17, 48000, 2, 4)
		// Insert in opposite order to prove canonicalization.
		_ = b.AddTrack(goodRecord("zzz"))
		_ = b.AddTrack(goodRecord("aaa"))
		return b.Build()
	}
	m1, m2 := build(), build()
	if m1.OutputSHA == "" || m1.OutputSHA != m2.OutputSHA {
		t.Fatalf("hashes not deterministic: %q vs %q", m1.OutputSHA, m2.OutputSHA)
	}
	if m1.Tracks[0].TrackID != "aaa" {
		t.Fatal("manifest tracks must be stably sorted")
	}
}

func TestMP4RoundTrip(t *testing.T) {
	clock, _ := timeline.NewClock(48000)
	b := NewBuilder(clock, 17, 48000, 2, 4)
	_ = b.AddTrack(goodRecord("t1"))
	b.AddFailure(FailureRecord{
		Code: timeline.ErrCodeTruncatedWAV, Stage: timeline.StageWAV, TrackID: "t2",
		Message: "data chunk declares 400 bytes but container ends",
		Checkpoint: timeline.Checkpoint{TrackID: "t2", BlockIndex: 2, Position: 300},
	})
	want := b.Build()
	seeker := newMemSeeker(nil)
	if err := WriteMP4(seeker, want); err != nil {
		t.Fatalf("write mp4: %v", err)
	}
	got, err := ReadMP4(bytes.NewReader(seeker.Bytes()))
	if err != nil {
		t.Fatalf("read mp4: %v", err)
	}
	if len(got.Tracks) != 1 || got.Tracks[0].TrackID != "t1" {
		t.Fatalf("round trip tracks = %+v", got.Tracks)
	}
	if got.OutputSHA != want.OutputSHA || got.Seed != 17 {
		t.Fatal("round trip lost manifest identity")
	}
	if len(got.Failures) != 1 || got.Failures[0].Stage != timeline.StageWAV ||
		got.Failures[0].Checkpoint.BlockIndex != 2 {
		t.Fatalf("failure record round trip = %+v", got.Failures)
	}
	if err := VerifyDuration(got, clock); err != nil {
		t.Fatalf("duration verification: %v", err)
	}
}

func TestReadMP4RejectsWrongBrand(t *testing.T) {
	seeker := newMemSeeker(nil)
	m := Manifest{Seed: 1}
	if err := WriteMP4(seeker, m); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMP4(bytes.NewReader(seeker.Bytes())); err != nil {
		// Empty-track manifest still carries the brand; only corrupt input fails.
		t.Fatalf("valid carrier rejected: %v", err)
	}
	// Corrupt the ftyp brand bytes (offset 8 holds the major brand).
	raw := seeker.Bytes()
	raw[8] = 'X'
	if _, err := ReadMP4(bytes.NewReader(raw)); err == nil {
		t.Fatal("carrier with corrupted brand must be rejected")
	}
}

func TestFailureRecordCarriesStageAndCheckpoint(t *testing.T) {
	clock, _ := timeline.NewClock(48000)
	b := NewBuilder(clock, 17, 48000, 2, 4)
	b.AddFailure(FailureRecord{
		Code: timeline.ErrCodeMissingBlock, Stage: timeline.StageTimeline, TrackID: "down",
		Message: "missing 200 samples",
		Checkpoint: timeline.Checkpoint{TrackID: "up", BlockIndex: 4, Position: 500},
	})
	m := b.Build()
	f := m.Failures[0]
	if f.Stage == "" || f.TrackID == "" || f.Checkpoint.Position != 500 {
		t.Fatalf("failure not locatable: %+v", f)
	}
}
