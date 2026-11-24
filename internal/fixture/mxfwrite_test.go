package fixture

import (
	"testing"

	"archive/internal/essence"
	"archive/internal/model"
)

func TestBuildMXFLayoutIsConsistent(t *testing.T) {
	s, ok := FindSpec(DefaultMixed(), "reel_a")
	if !ok {
		t.Fatal("reel_a spec missing")
	}
	layout, data, err := BuildMXF(s)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(data)) != layout.Size {
		t.Fatalf("size mismatch %d vs %d", len(data), layout.Size)
	}
	if layout.MediaStart >= layout.MediaEnd {
		t.Fatalf("media range %d..%d", layout.MediaStart, layout.MediaEnd)
	}
	for _, tl := range layout.Tracks {
		if len(tl.Frames) != tl.Spec.Frames {
			t.Fatalf("%s: got %d frames", tl.Spec.Logical, len(tl.Frames))
		}
		for _, f := range tl.Frames {
			if f.Offset+20 > uint64(len(data)) {
				t.Fatalf("frame out of range")
			}
			// KLV header: key then BER length.
			h := essenceKey(tl.Spec.Kind)
			if string(data[f.Offset:f.Offset+16]) != string(h[:]) {
				t.Fatalf("%s/%d: KLV key mismatch", tl.Spec.Logical, f.Seq)
			}
			if data[f.Offset+16] != 0x83 {
				t.Fatalf("%s/%d: BER form %#x", tl.Spec.Logical, f.Seq, data[f.Offset+16])
			}
			payloadStart := f.Offset + 20
			eh, err := essence.ParseHeader(data[payloadStart:])
			if err != nil {
				t.Fatalf("%s/%d: %v", tl.Spec.Logical, f.Seq, err)
			}
			if eh.Seq != f.Seq || eh.TrackID != tl.Spec.TrackID {
				t.Fatalf("%s/%d: header identity %+v", tl.Spec.Logical, f.Seq, eh)
			}
			if ok, complete := essence.VerifyPayload(data[payloadStart:payloadStart+f.Size], eh); !complete || !ok {
				t.Fatalf("%s/%d: bad payload", tl.Spec.Logical, f.Seq)
			}
		}
	}
}

func TestBuildMXFDeterministic(t *testing.T) {
	s, _ := FindSpec(DefaultMixed(), "reel_a")
	_, a, err := BuildMXF(s)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := BuildMXF(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("mxf fixture is not deterministic")
	}
}

func TestBuildMXFStartsWithPartitionPack(t *testing.T) {
	s, _ := FindSpec(DefaultMixed(), "reel_a")
	_, data, err := BuildMXF(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:16]) != string(keyHeaderMeta[:]) {
		t.Fatalf("file does not start with a header partition pack: %x", data[:16])
	}
	// The primer pack KLV must be found immediately after the partition pack.
	packLen := int(data[17])<<16 | int(data[18])<<8 | int(data[19])
	next := 20 + packLen
	if next+16 > len(data) || string(data[next:next+16]) != string(keyPrimerPack[:]) {
		t.Fatalf("primer pack not found at %d", next)
	}
	if s.Format != model.ContainerMXF {
		t.Fatal("format")
	}
}
