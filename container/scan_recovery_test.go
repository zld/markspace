package container

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// box writes one ISO-BMFF box with a small (8-byte) header.
func TestScanGoodFile(t *testing.T) {
	dir := t.TempDir()
	fx := writeFixture(t, dir, 0)
	result, err := ScanFile(fx.path, ScanOptions{})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !result.IndexComplete {
		t.Fatalf("index reported incomplete: %+v", result.Divergences)
	}
	if len(result.Divergences) != 0 {
		t.Fatalf("divergences = %+v", result.Divergences)
	}
	if len(result.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(result.Tracks))
	}
	video, audio := result.Tracks[0], result.Tracks[1]
	if video.Kind != TrackVideo || audio.Kind != TrackAudio {
		t.Fatalf("kinds = %v, %v", video.Kind, audio.Kind)
	}
	if len(video.Samples) != 20 || len(audio.Samples) != 40 {
		t.Fatalf("sample counts = %d, %d", len(video.Samples), len(audio.Samples))
	}
	// Video sample timestamps: 3000 ticks at 90000 Hz = 33.3 ms.
	if video.Samples[1].StartNS != 33_333_333 {
		t.Fatalf("sample 2 starts at %d", video.Samples[1].StartNS)
	}
	if video.Samples[0].StartNS != 0 || video.Samples[19].StartNS != 633_333_333 {
		t.Fatalf("video timeline: first=%d last=%d", video.Samples[0].StartNS, video.Samples[19].StartNS)
	}
	// Every sample must be inside the file.
	for _, track := range result.Tracks {
		for _, sample := range track.Samples {
			if sample.Offset+int64(sample.Size) > result.Size {
				t.Fatalf("sample %d/%d out of file: %d+%d > %d", track.ID, sample.Number, sample.Offset, sample.Size, result.Size)
			}
		}
	}
	// Sync flags come from stss for video and are implied for audio.
	if !video.Samples[0].Sync || video.Samples[1].Sync || !video.Samples[4].Sync {
		t.Fatalf("video sync flags wrong: %v %v %v", video.Samples[0].Sync, video.Samples[1].Sync, video.Samples[4].Sync)
	}
	if !audio.Samples[3].Sync {
		t.Fatal("audio samples should be sync")
	}
	if result.FirstUnreadable != -1 {
		t.Fatalf("FirstUnreadable = %d, want -1", result.FirstUnreadable)
	}
}

func TestScanIndexPastFileEnd(t *testing.T) {
	dir := t.TempDir()
	fx := writeFixture(t, dir, 0)
	fixtureData, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite with chunk offsets far past the end of the file.
	bad := writeFixture(t, dir, 1_000_000)
	result, err := ScanFile(bad.path, ScanOptions{})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if result.IndexComplete {
		t.Fatal("index should be reported incomplete")
	}
	if len(result.Divergences) == 0 {
		t.Fatal("expected divergences")
	}
	if !result.RecoveredWithinFile() {
		t.Fatalf("first unreadable %d outside file size %d", result.FirstUnreadable, result.Size)
	}
	// The original data is intact, so boundary recovery still yields samples.
	if result.RecoveredSamples == 0 {
		t.Fatal("expected boundary-recovered samples")
	}
	for _, track := range result.Tracks {
		for _, sample := range track.Samples {
			if sample.Offset+int64(sample.Size) > result.Size {
				t.Fatalf("recovered sample outside file")
			}
		}
	}
	_ = fixtureData
}

func TestScanTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	fx := writeFixture(t, dir, 0)
	full, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{len(full) - 100, len(full) / 2, 40, 9} {
		path := filepath.Join(dir, fmt.Sprintf("cut-%d.mp4", cut))
		if err := os.WriteFile(path, full[:cut], 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := ScanFile(path, ScanOptions{})
		if err != nil {
			t.Fatalf("cut %d: ScanFile: %v", cut, err)
		}
		if result.RecoveredWithinFile() == false {
			t.Fatalf("cut %d: first unreadable %d, size %d", cut, result.FirstUnreadable, result.Size)
		}
		if cut > 8 && result.FirstUnreadable < 0 {
			t.Fatalf("cut %d: expected a truncation offset", cut)
		}
		if result.FirstUnreadable > result.Size {
			t.Fatalf("cut %d: first unreadable %d > size %d", cut, result.FirstUnreadable, result.Size)
		}
		for _, track := range result.Tracks {
			for _, sample := range track.Samples {
				if sample.Offset+int64(sample.Size) > result.Size {
					t.Fatalf("cut %d: sample past EOF", cut)
				}
			}
		}
	}
}

func TestScanMissingMoov(t *testing.T) {
	dir := t.TempDir()
	// ftyp followed by mdat with no moov at all.
	payload := bytes.Repeat([]byte{0xAB}, 8192)
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header, uint32(8+len(payload)))
	copy(header[4:], "mdat")
	path := filepath.Join(dir, "nomoov.mp4")
	if err := os.WriteFile(path, cat(ftyp(), header, payload), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ScanFile(path, ScanOptions{})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if result.IndexComplete {
		t.Fatal("index should be incomplete without moov")
	}
	if result.RecoveredSamples == 0 {
		t.Fatal("expected boundary recovery without moov")
	}
	if result.RecoveredBytes != int64(len(payload)) {
		t.Fatalf("recovered bytes = %d, want %d", result.RecoveredBytes, len(payload))
	}
	if !result.RecoveredWithinFile() {
		t.Fatalf("first unreadable %d outside size %d", result.FirstUnreadable, result.Size)
	}
}

func TestScanGarbageResync(t *testing.T) {
	dir := t.TempDir()
	// A corrupt byte run before the real structure must not stop the scan.
	payload := bytes.Repeat([]byte{0x11}, 2048)
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header, uint32(8+len(payload)))
	copy(header[4:], "mdat")
	path := filepath.Join(dir, "garbage.mp4")
	data := cat(bytes.Repeat([]byte{0xFF}, 100), ftyp(), header, payload)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ScanFile(path, ScanOptions{})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if result.FirstUnreadable != 0 {
		t.Fatalf("first unreadable = %d, want 0", result.FirstUnreadable)
	}
	if len(result.Boxes) < 2 {
		t.Fatalf("resync found %d boxes", len(result.Boxes))
	}
	if result.RecoveredBytes == 0 {
		t.Fatal("expected recovery of mdat past the garbage")
	}
}

func TestScanEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanFile(path, ScanOptions{}); err == nil {
		t.Fatal("expected an error for an empty file")
	}
	if _, err := ScanFile(filepath.Join(dir, "missing.mp4"), ScanOptions{}); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestScanDeterministic(t *testing.T) {
	dir := t.TempDir()
	fx := writeFixture(t, dir, 0)
	a, err := ScanFile(fx.path, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ScanFile(fx.path, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Tracks) != len(b.Tracks) {
		t.Fatal("track count differs between runs")
	}
	for i := range a.Tracks {
		if len(a.Tracks[i].Samples) != len(b.Tracks[i].Samples) {
			t.Fatal("sample count differs between runs")
		}
		for j := range a.Tracks[i].Samples {
			if a.Tracks[i].Samples[j] != b.Tracks[i].Samples[j] {
				t.Fatalf("sample %d/%d differs", i, j)
			}
		}
	}
}
