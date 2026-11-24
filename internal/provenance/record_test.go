package provenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/index"
	mp4 "mediad/internal/mp4"
)

func TestHashFileDeterministic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte("hello media"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := HashFile(p)
	if a != b {
		t.Fatal("hash unstable")
	}
	if len(a) != 64 {
		t.Fatalf("digest length %d", len(a))
	}
}

func TestRecordDigestIsStableAndSensitive(t *testing.T) {
	mk := func(repairs int) *Record {
		tr := &mp4.RepairedTrack{
			TrackID: 1, Timescale: 90000, Kind: "video", SyncFromFlags: true,
			EditList: clock.EditList{MediaTimescale: 90000, MovieTimescale: 1000,
				Entries: []clock.EditEntry{{SegmentDuration: 12000, MediaTime: 0}}},
		}
		for i := 0; i < repairs; i++ {
			tr.Intervals = append(tr.Intervals, mp4.RepairInterval{
				TrackID: 1, Cause: clock.CodeDTSRegression, FirstIndex: uint32(i + 1),
				LastIndex: uint32(i + 1), RawOffset: 100 + int64(i), CumulativeShiftTicks: 3000, Count: 1,
			})
		}
		ti := &index.TrackIndex{TrackID: 1, Timescale: 90000, CoveragePct: 100}
		cam, err := BuildCamera("cam-a", "media/cam-a.mp4", 1, "deadbeef", tr, ti)
		if err != nil {
			t.Fatal(err)
		}
		r := &Record{Seed: 42, TargetBps: 8_000_000, ManifestSHA: "abc", Cameras: []CameraRecord{cam}}
		if _, err := r.Finalize(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := mk(0)
	b := mk(0)
	if a.Digest != b.Digest {
		t.Fatalf("identical records hashed differently: %s vs %s", a.Digest, b.Digest)
	}
	c := mk(1)
	if c.Digest == a.Digest {
		t.Fatal("different repairs produced the same digest")
	}
}

func TestCameraRecordCarriesLineAndInput(t *testing.T) {
	tr := &mp4.RepairedTrack{TrackID: 3, Timescale: 48000, Kind: "audio"}
	tr.Intervals = []mp4.RepairInterval{{Cause: clock.CodeDTSRegression, RawOffset: 777, Count: 2}}
	ti := &index.TrackIndex{TrackID: 3, CoveragePct: 100, DerivedSync: false}
	cam, err := BuildCamera("cam-b", "media/cam-b.mp4", 2, "sha", tr, ti)
	if err != nil {
		t.Fatal(err)
	}
	if cam.ManifestLine != 2 || cam.InputSHA != "sha" || cam.Camera != "cam-b" {
		t.Fatalf("record lost provenance: %+v", cam)
	}
	if len(cam.Repairs) != 1 || cam.Repairs[0].RawOffset != 777 {
		t.Fatalf("repairs lost: %+v", cam.Repairs)
	}
	if cam.IndexVersion != index.Version || cam.IndexSHA == "" {
		t.Fatalf("index provenance missing: %+v", cam)
	}
}

func TestRecordWriteIsAtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	r := &Record{Seed: 1, TargetBps: 1}
	if _, err := r.Finalize(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "provenance.json")
	if err := r.Write(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Digest != r.Digest {
		t.Fatalf("digest mismatch after round trip")
	}
	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "provenance.json" {
			t.Fatalf("stray file %s", e.Name())
		}
	}
}
