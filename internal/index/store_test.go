package index

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediatool/internal/fault"
	"mediatool/internal/fixture"
)

func scanFixture(t *testing.T, seed int64, loss, trunc int) *ScanResult {
	t.Helper()
	opts := fixture.DemoOptions(seed, 4, 1, loss, trunc)
	opts.DurationMs = 3000
	cap, err := fixture.Generate(opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tracks := map[uint32]TrackInfo{}
	kinds := map[uint32]string{}
	for _, s := range opts.Tracks {
		tracks[s.SSRC] = TrackInfo{TrackID: s.TrackID, ClockRate: s.ClockRate}
		kinds[s.TrackID] = s.Kind
	}
	res, err := ScanReader(bytes.NewReader(cap.Bytes), ScanConfig{
		Tracks: tracks, Kinds: kinds, Window: 64, IsKeyframe: IsVideoKeyframe,
		Digest: DigestBytes(cap.Bytes), InputBytes: int64(len(cap.Bytes)),
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return res
}

func TestIndexRecordsPerTrackFacts(t *testing.T) {
	res := scanFixture(t, 3, 0, 0)
	ix := res.Index
	if len(ix.Tracks) == 0 {
		t.Fatal("no tracks indexed")
	}
	for _, tr := range ix.Tracks {
		if tr.Packets.Packets == 0 {
			t.Fatalf("track %d has no packet range", tr.TrackID)
		}
		if tr.Packets.LastOffset < tr.Packets.FirstOffset {
			t.Fatalf("track %d packet range inverted", tr.TrackID)
		}
		if !tr.Time.Valid {
			t.Fatalf("track %d has no time range", tr.TrackID)
		}
		if tr.Time.EndMs < tr.Time.StartMs {
			t.Fatalf("track %d time range inverted", tr.TrackID)
		}
		if tr.ClockRate == 0 {
			t.Fatalf("track %d has no sample rate", tr.TrackID)
		}
	}
	// Video and audio tracks must both contribute keyframes.
	var videoKF, audioKF int
	for _, tr := range ix.Tracks {
		if tr.Kind == "video" {
			videoKF += len(tr.Keyframes)
		} else {
			audioKF += len(tr.Keyframes)
		}
	}
	if videoKF == 0 || audioKF == 0 {
		t.Fatalf("missing keyframes: video=%d audio=%d", videoKF, audioKF)
	}
}

func TestIndexChecksumDetectsTampering(t *testing.T) {
	res := scanFixture(t, 3, 0, 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := res.Index.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, res.Index.InputDigest); err != nil {
		t.Fatalf("clean load: %v", err)
	}
	// Flip a byte inside the document body but leave the checksum string.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		if data[i] == '"' && i+6 < len(data) && string(data[i+1:i+6]) == "track" {
			data[i+1] = 'X'
			break
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, res.Index.InputDigest); err == nil {
		t.Fatal("tampered index was accepted")
	} else if !fault.IsKind(err, fault.KindIndexDigest) {
		t.Fatalf("expected index_digest fault, got %v", err)
	}
}

func TestIndexRejectsDifferentInputDigest(t *testing.T) {
	res := scanFixture(t, 3, 0, 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := res.Index.Save(path); err != nil {
		t.Fatal(err)
	}
	other := DigestBytes([]byte("some other input"))
	_, err := Load(path, other)
	if err == nil {
		t.Fatal("index bound to a different input was accepted")
	}
	if !fault.IsKind(err, fault.KindIndexDigest) {
		t.Fatalf("expected index_digest fault, got %v", err)
	}
}

func TestIndexIsDeterministic(t *testing.T) {
	a := scanFixture(t, 11, 2, 0)
	b := scanFixture(t, 11, 2, 0)
	ea, err := a.Index.Encode()
	if err != nil {
		t.Fatal(err)
	}
	eb, err := b.Index.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ea, eb) {
		t.Fatal("two scans of the same input produced different index bytes")
	}
}

func TestNormalizeAudioRejectsUnknownLayout(t *testing.T) {
	_, err := NormalizeAudio(1, "atmos", 48000, "", 0, 77)
	if err == nil {
		t.Fatal("expected unknown layout error")
	}
	if !fault.IsKind(err, fault.KindLayout) {
		t.Fatalf("expected layout fault, got %v", err)
	}
	f, _ := fault.As(err)
	if f.Offset != 77 {
		t.Fatalf("fault not located: %+v", f)
	}
}

func TestNormalizeAudioRejectsNonIntegralRate(t *testing.T) {
	_, err := NormalizeAudio(1, "stereo", 44100, "stereo", 48000, 99)
	if err == nil {
		t.Fatal("expected non-integral rate error")
	}
	if !fault.IsKind(err, fault.KindSampleRate) {
		t.Fatalf("expected sample_rate fault, got %v", err)
	}
}
