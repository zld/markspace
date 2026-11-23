package segment

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"

	"mediqc/internal/fault"
)

// buildMP4 writes a minimal MP4: ftyp + moov/trak/mdia/minf/stbl(+stss)
// and optionally a moof/traf/trun carrying per-sample sync flags.
func buildMP4(t *testing.T, withStss bool, samples, syncEvery int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "asset.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := mp4.NewWriter(f)
	mustBox := func(bi *mp4.BoxInfo, err error) *mp4.BoxInfo {
		if err != nil {
			t.Fatal(err)
		}
		return bi
	}
	mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}))
	mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}))
	mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}))
	mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}))
	mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}))
	if withStss {
		var nums []uint32
		for i := 0; i < samples; i += syncEvery {
			nums = append(nums, uint32(i+1))
		}
		mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}))
		stss := &mp4.Stss{EntryCount: uint32(len(nums)), SampleNumber: nums}
		if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.EndBox(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := w.EndBox(); err != nil {
			t.Fatal(err)
		}
	}
	if !withStss {
		mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}))
		mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}))
		trun := &mp4.Trun{
			FullBox:     mp4.FullBox{Version: 0, Flags: [3]byte{0x00, 0x04, 0x00}},
			SampleCount: uint32(samples),
		}
		for i := 0; i < samples; i++ {
			var flags uint32
			if i%syncEvery != 0 {
				flags = nonSyncSampleFlag
			}
			trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleFlags: flags})
		}
		mustBox(w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrun()}))
		if _, err := mp4.Marshal(w, trun, mp4.Context{}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.EndBox(); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := w.EndBox(); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

func TestScanStssIndex(t *testing.T) {
	path := buildMP4(t, true, 100, 24)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kfs, src, err := ScanKeyframes(f)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceIndex {
		t.Fatalf("source = %q, want %q", src, SourceIndex)
	}
	want := []int64{0, 24, 48, 72, 96}
	if len(kfs) != len(want) {
		t.Fatalf("keyframes %v, want %v", kfs, want)
	}
	for i := range want {
		if kfs[i] != want[i] {
			t.Fatalf("keyframes %v, want %v", kfs, want)
		}
	}
}

func TestScanSampleFlagsWhenIndexMissing(t *testing.T) {
	path := buildMP4(t, false, 10, 3)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kfs, src, err := ScanKeyframes(f)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceScan {
		t.Fatalf("source = %q, want %q", src, SourceScan)
	}
	want := []int64{0, 3, 6, 9}
	if len(kfs) != len(want) {
		t.Fatalf("keyframes %v, want %v", kfs, want)
	}
	for i := range want {
		if kfs[i] != want[i] {
			t.Fatalf("keyframes %v, want %v", kfs, want)
		}
	}
}

func TestScanEmptyAssetFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _, err = ScanKeyframes(f)
	if !fault.IsCode(err, fault.CodeMissingKeyframe) {
		t.Fatalf("want E_MISSING_KEYFRAME, got %v", err)
	}
}
