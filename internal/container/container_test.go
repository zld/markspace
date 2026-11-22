package container

import (
	"os"
	"path/filepath"
	"testing"

	"mediatimeline/internal/clock"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFixtureRoundTrip(t *testing.T) {
	spec := FixtureSpec{
		Seed: 42, Timescale: 44100, SampleCount: 12, FramesPerSamp: 256,
		Channels: 2, VFRJitter: 12, DriftPPM: 350,
	}
	data, err := BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "vfr.m4a", data)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tracks, err := s.ScanTracks()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(tracks) != 1 || tracks[0].TrackID != 1 || tracks[0].Handler != "soun" {
		t.Fatalf("tracks=%+v", tracks)
	}
	got := tracks[0].Entries
	if len(got) != spec.SampleCount {
		t.Fatalf("samples=%d want %d", len(got), spec.SampleCount)
	}
	// VFR: raw deltas must not all be equal; nanos must be strictly increasing.
	allEqual := true
	for i := 1; i < len(got); i++ {
		if got[i].DTSNanos <= got[i-1].DTSNanos {
			t.Fatal("non-monotonic DTS")
		}
		if got[i].DurationRaw != got[i-1].DurationRaw {
			allEqual = false
		}
		if got[i].FileOffset != got[i-1].FileOffset+got[i-1].PayloadSize {
			t.Fatal("chunk offsets not contiguous")
		}
	}
	if allEqual {
		t.Fatal("VFR jitter did not materialize in durations")
	}
	if got[0].PayloadCRC32 == 0 {
		t.Fatal("CRC not computed")
	}
}

func TestTruncatedSTBL(t *testing.T) {
	data, err := BuildMP4(FixtureSpec{
		Seed: 1, Timescale: 48000, SampleCount: 6, FramesPerSamp: 128, Channels: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := TruncateSTBL(data)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "trunc.m4a", corrupt)
	s, _ := Open(path)
	defer s.Close()
	_, err = s.ScanTracks()
	var ce *clock.Error
	if err == nil {
		t.Fatal("expected error on truncated stbl")
	}
	if !asClock(err, &ce) || ce.Code != clock.ErrTruncatedBox {
		t.Fatalf("want TRUNCATED_BOX, got %v", err)
	}
}

func TestIllegalOffset(t *testing.T) {
	data, err := BuildMP4(FixtureSpec{
		Seed: 2, Timescale: 48000, SampleCount: 5, FramesPerSamp: 64, Channels: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Point the first chunk offset past end-of-file.
	idx, _, err := findBox(data, "stco")
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), data...)
	// stco payload: 4 version/flags + 4 count + entries
	putU32At(corrupt, idx+8+4+4, uint32(len(data)+4096))
	path := writeTemp(t, "badoff.m4a", corrupt)
	s, _ := Open(path)
	defer s.Close()
	_, err = s.ScanTracks()
	var ce *clock.Error
	if !asClock(err, &ce) || ce.Code != clock.ErrIllegalOffset {
		t.Fatalf("want ILLEGAL_OFFSET, got %v", err)
	}
	if ce.SampleIndex != 0 || ce.TrackID != 1 || ce.FileOffset < 0 {
		t.Fatalf("localization lost: %+v", ce)
	}
}

func TestCRCCorruptionLocalized(t *testing.T) {
	data, err := BuildMP4(FixtureSpec{
		Seed: 3, Timescale: 48000, SampleCount: 8, FramesPerSamp: 96, Channels: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	corrupt, off, err := CorruptCRC(data, 4)
	if err != nil {
		t.Fatal(err)
	}
	cleanPath := writeTemp(t, "clean.m4a", data)
	corruptPath := writeTemp(t, "crc.m4a", corrupt)
	sc, _ := Open(cleanPath)
	cleanTracks, err := sc.ScanTracks()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := Open(corruptPath)
	defer s.Close()
	tracks, err := s.ScanTracks()
	if err != nil {
		t.Fatalf("scan must still succeed: %v", err)
	}
	e := tracks[0].Entries[4]
	if e.FileOffset != off {
		t.Fatalf("offset=%d want %d", e.FileOffset, off)
	}
	if e.PayloadCRC32 == cleanTracks[0].Entries[4].PayloadCRC32 {
		t.Fatal("corrupted payload kept the clean CRC")
	}
}

func putU32At(b []byte, at int, v uint32) {
	b[at] = byte(v >> 24)
	b[at+1] = byte(v >> 16)
	b[at+2] = byte(v >> 8)
	b[at+3] = byte(v)
}

func asClock(err error, target **clock.Error) bool {
	for err != nil {
		if c, ok := err.(*clock.Error); ok {
			*target = c
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
