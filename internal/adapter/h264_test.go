package adapter

import (
	"bytes"
	"testing"

	"archive-recovery/internal/media"
)

// buildNAL wraps an RBFL payload with a chosen start code.
func buildNAL(sc []byte, nalType byte, payload []byte) []byte {
	out := append([]byte{}, sc...)
	out = append(out, nalType) // header byte; bit pattern irrelevant to type
	out = append(out, payload...)
	return out
}

func TestH264Classification(t *testing.T) {
	tc, frame, key := classifyH264([]byte{0x65, 0x00}) // IDR, nal_ref_idc=3
	if tc != 5 || !frame || !key {
		t.Fatalf("IDR classification wrong: %d %v %v", tc, frame, key)
	}
	tc, frame, key = classifyH264([]byte{0x41, 0x00}) // non-IDR slice
	if tc != 1 || !frame || key {
		t.Fatalf("P-slice classification wrong: %d %v %v", tc, frame, key)
	}
	tc, frame, key = classifyH264([]byte{0x67, 0x00}) // SPS
	if tc != 7 || frame || key {
		t.Fatalf("SPS classification wrong: %d %v %v", tc, frame, key)
	}
}

func TestH264SplitterFullStream(t *testing.T) {
	stream := bytes.Join([][]byte{
		buildNAL([]byte{0, 0, 0, 1}, 0x67, []byte{1, 2, 3}),       // SPS
		buildNAL([]byte{0, 0, 1}, 0x68, []byte{4}),                // PPS, 3-byte SC
		buildNAL([]byte{0, 0, 0, 1}, 0x65, bytes.Repeat([]byte{9}, 10)), // IDR
		buildNAL([]byte{0, 0, 1}, 0x41, bytes.Repeat([]byte{7}, 8)),    // P slice
	}, nil)

	a, err := Get("h264", H264Version)
	if err != nil {
		t.Fatal(err)
	}
	sp := a.NewSplitter()
	units, uerrs, err := sp.Write(stream)
	if err != nil || len(uerrs) != 0 {
		t.Fatalf("unexpected errors: %v %v", err, uerrs)
	}
	tail, dangling := sp.Close()
	units = append(units, tail...)
	if dangling != nil {
		t.Fatalf("unexpected dangling unit: %+v", dangling)
	}
	if len(units) != 4 {
		t.Fatalf("want 4 units, got %d", len(units))
	}
	if !units[2].IsKeyframe || !units[2].IsFrame {
		t.Fatal("third unit must be IDR keyframe")
	}
	if units[3].IsKeyframe || !units[3].IsFrame {
		t.Fatal("fourth unit must be non-key frame")
	}
	if units[0].Offset != 0 || units[1].Offset != 8 {
		t.Fatalf("bad offsets: %d %d", units[0].Offset, units[1].Offset)
	}
	if got := units[0].Offset + units[0].Length; got != units[1].Offset {
		t.Fatalf("unit 0 length does not reach unit 1: %d", got)
	}
}

func TestH264SplitterTruncation(t *testing.T) {
	good := buildNAL([]byte{0, 0, 0, 1}, 0x65, []byte{1, 2, 3})
	truncated := []byte{0, 0, 0, 1, 0x65} // header then EOF
	stream := append(append([]byte{}, good...), truncated...)

	sp := NewAnnexBScanner(classifyH264, h264MaxUnit)
	units, _, err := sp.Write(stream)
	if err != nil {
		t.Fatal(err)
	}
	tail, dangling := sp.Close()
	units = append(units, tail...)
	if len(units) != 1 || !units[0].IsKeyframe {
		t.Fatalf("good unit missing: %+v", units)
	}
	if dangling == nil || dangling.Class != media.ClassRecoverable {
		t.Fatalf("want recoverable dangling error, got %+v", dangling)
	}
	if dangling.Offset != int64(len(good)) {
		t.Fatalf("dangling offset %d want %d", dangling.Offset, len(good))
	}
}

func TestH264SplitterAcrossChunks(t *testing.T) {
	stream := bytes.Join([][]byte{
		buildNAL([]byte{0, 0, 0, 1}, 0x65, []byte{1, 2, 3, 4}),
		buildNAL([]byte{0, 0, 1}, 0x41, []byte{5, 6}),
	}, nil)
	sp := NewAnnexBScanner(classifyH264, h264MaxUnit)
	var all []Unit
	for i := 0; i < len(stream); i++ {
		units, _, err := sp.Write(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, units...)
	}
	tail, dangling := sp.Close()
	if dangling != nil {
		t.Fatal(dangling)
	}
	all = append(all, tail...)
	if len(all) != 2 {
		t.Fatalf("want 2 units regardless of chunking, got %d", len(all))
	}
}

func TestRegistryUnknownVersionFails(t *testing.T) {
	if _, err := Get("h264", "h264-99"); err == nil {
		t.Fatal("unknown version must fail explicitly")
	}
	if _, err := Get("h264", H264Version); err != nil {
		t.Fatal(err)
	}
}
