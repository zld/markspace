package event

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestUnwrapperCrossesUint32Wrap(t *testing.T) {
	u := Unwrapper{}
	first := u.Unwrap(math.MaxUint32 - 10)
	wrapped := u.Unwrap(9) // 11 ticks after MaxUint32-1? Sequence: max-10 -> ... max ->0 ...9 =20.
	if first != math.MaxUint32-10 {
		t.Fatalf("first = %d", first)
	}
	if wrapped-first != 20 {
		t.Fatalf("delta across wrap = %d, want 20 (wrapped=%d)", wrapped-first, wrapped)
	}

	base := NearWrapBase()
	u = Unwrapper{}
	origin := u.Unwrap(base)
	target := u.Unwrap(WrappedTimestampAt(base, 90_000))
	if target-origin != 90_000 {
		t.Fatalf("fixture wrap delta = %d, want 90000", target-origin)
	}
}

func TestPacketRoundTripAndOffsets(t *testing.T) {
	pkt, err := MarshalPacket(7, 123456, "kill", "kill-42")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	off, err := WriteRecord(&buf, pkt)
	if err != nil {
		t.Fatal(err)
	}
	// bytes.Buffer is not a Seeker; fallback reports the in-record packet offset.
	if off != 4 {
		t.Fatalf("buffer packet offset = %d, want 4", off)
	}

	path := filepath.Join(t.TempDir(), "events.rtp")
	var fileBuf bytes.Buffer
	if _, err := WriteRecord(&fileBuf, pkt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fileBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "kill-42" || events[0].RawOffset != 4 {
		t.Fatalf("unexpected events: %#v", events)
	}
}
