package mp4seg

import (
	"bytes"
	"testing"

	"gamepack/internal/clock"
	"gamepack/internal/stream"
)

func samplesFor(codec stream.Codec, n int) []Sample {
	out := make([]Sample, n)
	for i := 0; i < n; i++ {
		out[i] = Sample{
			Unit: stream.Unit{
				Ordinal: i, Start: int64(i * 4), End: int64(i*4 + 4),
				Data: []byte{0x65, byte(i), 0xaa, 0xbb},
			},
			Tick:  clock.Tick(i * 3000),
			IsKey: i == 0,
			Codec: codec,
		}
	}
	out[0].ConfigData = []byte{0x01, 0x02, 0x03}
	return out
}

func TestWriteAndValidateH264(t *testing.T) {
	raw, err := renderToBytes(samplesFor(stream.CodecH264, 4))
	if err != nil {
		t.Fatalf("mux: %v", err)
	}
	if len(raw) == 0 || string(raw[4:8]) != "ftyp" {
		t.Fatal("output does not start with an ftyp box")
	}
	rep, err := ValidateFragment(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !rep.OK || rep.ChecksFailed != 0 {
		t.Fatalf("report not OK: %+v", rep)
	}
	if rep.Timescale != uint32(clock.TicksPerSecond) {
		t.Fatalf("timescale = %d", rep.Timescale)
	}
	if rep.SampleCount != 4 {
		t.Fatalf("sample count = %d, want 4", rep.SampleCount)
	}
	if rep.SHA256 != Checksum(raw) {
		t.Fatal("checksum mismatch")
	}
}

func TestWriteAllCodecsAreValid(t *testing.T) {
	for _, codec := range []stream.Codec{stream.CodecH264, stream.CodecH265, stream.CodecAV1} {
		raw, err := renderToBytes(samplesFor(codec, 3))
		if err != nil {
			t.Fatalf("%s mux: %v", codec, err)
		}
		rep, err := ValidateFragment(bytes.NewReader(raw))
		if err != nil || !rep.OK {
			t.Fatalf("%s validate failed: %v rep=%+v", codec, err, rep)
		}
	}
}

func TestDeterministicOutput(t *testing.T) {
	a, _ := renderToBytes(samplesFor(stream.CodecH264, 4))
	b, _ := renderToBytes(samplesFor(stream.CodecH264, 4))
	if Checksum(a) != Checksum(b) {
		t.Fatal("identical inputs must yield byte-identical fragments")
	}
}

func TestEmptyFragmentRejected(t *testing.T) {
	if err := WriteFragment(&bufSeeker{}, nil); err == nil {
		t.Fatal("empty fragment must be rejected")
	}
}

func TestCorruptedFragmentFailsValidation(t *testing.T) {
	raw, _ := renderToBytes(samplesFor(stream.CodecH264, 3))
	// Truncate mid-box to break the structure.
	bad := raw[:len(raw)/2]
	if _, err := ValidateFragment(bytes.NewReader(bad)); err == nil {
		t.Fatal("truncated fragment must fail validation")
	}
}
