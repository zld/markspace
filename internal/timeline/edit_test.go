package timeline

import (
	"encoding/binary"
	"errors"
	"testing"
)

func elstV0(entries ...Edit) []byte {
	out := []byte{0, 0, 0, 0} // version 0, flags
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], uint32(len(entries)))
	out = append(out, cnt[:]...)
	for _, e := range entries {
		var b [12]byte
		binary.BigEndian.PutUint32(b[0:4], uint32(e.SegmentDuration))
		binary.BigEndian.PutUint32(b[4:8], uint32(int32(e.MediaTime)))
		binary.BigEndian.PutUint32(b[8:12], uint32(e.Rate))
		out = append(out, b[:]...)
	}
	return out
}

const rate1 = 0x00010000

func TestEmptyEditThenMedia(t *testing.T) {
	data := elstV0(
		Edit{SegmentDuration: 9000, MediaTime: -1, Rate: 0},
		Edit{SegmentDuration: 45000, MediaTime: 81000, Rate: rate1},
	)
	edits, err := ParseElst(data, "moov/trak/edts/elst")
	if err != nil {
		t.Fatal(err)
	}
	m := NewMapper(edits)
	// Media 81000 -> movie 9000 (after the empty edit).
	got, ok := m.MediaToMovie(81000)
	if !ok || got != 9000 {
		t.Fatalf("MediaToMovie(81000) = %d,%v", got, ok)
	}
	// Media 90000 -> movie 18000.
	got, ok = m.MediaToMovie(90000)
	if !ok || got != 18000 {
		t.Fatalf("MediaToMovie(90000) = %d,%v", got, ok)
	}
	// Before the media edit: not presented.
	if _, ok := m.MediaToMovie(80000); ok {
		t.Fatal("media 80000 should not be presented")
	}
	// Movie time inside the empty edit: no media.
	if _, ok := m.MovieToMedia(4500); ok {
		t.Fatal("movie 4500 is inside empty edit")
	}
	// Movie 18000 -> media 90000.
	got, ok = m.MovieToMedia(18000)
	if !ok || got != 90000 {
		t.Fatalf("MovieToMedia(18000) = %d,%v", got, ok)
	}
	if d := m.MovieDuration(); d != 54000 {
		t.Fatalf("duration = %d", d)
	}
}

func TestUnsupportedRateHasPath(t *testing.T) {
	data := elstV0(Edit{SegmentDuration: 100, MediaTime: 0, Rate: 0x00008000})
	_, err := ParseElst(data, "moov/trak/edts/elst")
	if !errors.Is(err, ErrUnsupportedRate) {
		t.Fatalf("want ErrUnsupportedRate, got %v", err)
	}
	if got := err.Error(); !contains(got, "moov/trak/edts/elst") {
		t.Fatalf("error lacks box path: %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestIdentityMapper(t *testing.T) {
	m := NewMapper(nil)
	if got, ok := m.MediaToMovie(42); !ok || got != 42 {
		t.Fatalf("got %d,%v", got, ok)
	}
	if got, ok := m.MovieToMedia(7); !ok || got != 7 {
		t.Fatalf("got %d,%v", got, ok)
	}
}

func TestTruncatedElst(t *testing.T) {
	data := elstV0(Edit{SegmentDuration: 1, MediaTime: 0, Rate: rate1})
	if _, err := ParseElst(data[:len(data)-3], "x/elst"); err == nil {
		t.Fatal("truncated elst accepted")
	}
}
