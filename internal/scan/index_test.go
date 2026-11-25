package scan_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/maptrack"
	"mediacore/internal/remux"
	"mediacore/internal/scan"
)

func writeIndexFixtureMP4(t *testing.T, path string, marker byte) *scan.Index {
	t.Helper()
	idx := &scan.Index{
		Version: scan.IndexVersion, SourceSHA256: "memory", Params: scan.DefaultParams(),
		Tracks: []scan.Track{{ID: 1, Handler: "vide", Kind: "video", Codec: "mp4v", Timescale: 24000, Width: 64, Height: 48}},
	}
	var samples []scan.SampleRef
	tb := clock.Timebase{Timescale: 24000}
	for i := uint32(0); i < 4; i++ {
		dts := int64(i) * 1000
		data := []byte{marker, byte(i), 0, 0}
		samples = append(samples, scan.SampleRef{
			TrackID: 1, Index: i, Size: uint32(len(data)), Data: data,
			DTSUnits: dts, PTSUnits: dts, DurationUnits: 1000, DTSNS: tb.ToNS(dts), PTSNS: tb.ToNS(dts),
		})
	}
	mappings, err := maptrack.Map(idx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remux.WriteMP4(idx, mappings, samples, remux.Options{OutputPath: path, AllowedRoot: filepath.Dir(path), Resume: true}); err != nil {
		t.Fatal(err)
	}
	built, err := scan.BuildIndex(path, scan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestMP4IndexTracksSamplesAndTruncationOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.mp4")
	writeIndexFixtureMP4(t, path, 0x7a)

	idx, err := scan.BuildIndex(path, scan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Tracks) != 1 || idx.Tracks[0].Codec != "mp4v" || idx.Tracks[0].Timescale != 24000 {
		t.Fatalf("unexpected tracks: %+v", idx.Tracks)
	}
	if len(idx.Samples) != 4 {
		t.Fatalf("samples=%d want 4", len(idx.Samples))
	}
	for i, s := range idx.Samples {
		if s.Index != uint32(i) || s.Size != 4 || s.DurationUnits != 1000 {
			t.Fatalf("bad sample %d: %+v", i, s)
		}
	}

	trunc := filepath.Join(dir, "truncated.mp4")
	if err := os.Link(path, trunc); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(trunc, 40); err != nil {
		t.Fatal(err)
	}
	_, err = scan.BuildIndex(trunc, scan.DefaultParams())
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.TruncatedData || fe.Offset != 24 {
		t.Fatalf("err=%#v want truncated_data at offset 24", err)
	}
}

func TestIndexCacheDigestInvalidation(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mp4")
	b := filepath.Join(dir, "b.mp4")
	writeIndexFixtureMP4(t, a, 1)
	writeIndexFixtureMP4(t, b, 2)
	cache := filepath.Join(dir, "cache", "index.json")
	p := scan.DefaultParams()

	idx, rebuilt, err := scan.LoadOrBuild(a, cache, p)
	if err != nil || !rebuilt {
		t.Fatalf("first build rebuilt=%v err=%v", rebuilt, err)
	}
	if idx.SourceSHA256 == "" {
		t.Fatal("missing digest")
	}
	if _, rebuilt, err := scan.LoadOrBuild(a, cache, p); err != nil || rebuilt {
		t.Fatalf("unchanged reuse rebuilt=%v err=%v", rebuilt, err)
	}

	bIdx, err := scan.BuildIndex(b, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.SaveCache(cache, bIdx); err != nil {
		t.Fatal(err)
	}
	aAgain, rebuilt, err := scan.LoadOrBuild(a, cache, p)
	if err != nil || !rebuilt {
		t.Fatalf("digest change should rebuild: rebuilt=%v err=%v", rebuilt, err)
	}
	if aAgain.SourceSHA256 == bIdx.SourceSHA256 {
		t.Fatal("rebuilt index has stale digest")
	}
}

func TestRTPIndexUnknownSSRCAndTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rtp.bin")
	streams := []scan.RTPStreamMeta{{SSRC: 42, TrackID: 7, Codec: "mcrv", Kind: "video", Timescale: 90000}}
	err := scan.WriteRTPFixture(path, scan.RTPFixtureSpec{
		Streams: streams,
		Packets: []scan.RTPPacketSpec{
			{SSRC: 42, PayloadTS: 96, Sequence: 1, Timestamp: 9000, Marker: true, Data: []byte("one")},
			{SSRC: 42, PayloadTS: 96, Sequence: 2, Timestamp: 18000, Marker: true, Data: []byte("two")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := scan.BuildIndex(path, scan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Packets) != 2 || len(idx.Tracks) != 1 {
		t.Fatalf("packets=%d tracks=%d", len(idx.Packets), len(idx.Tracks))
	}

	bad := filepath.Join(dir, "unknown-ssrc.bin")
	if err := scan.WriteRTPFixture(bad, scan.RTPFixtureSpec{
		Streams: streams,
		Packets: []scan.RTPPacketSpec{{SSRC: 99, Sequence: 1, Timestamp: 1, Data: []byte("x")}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = scan.BuildIndex(bad, scan.DefaultParams())
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.UnsupportedTrack || fe.Offset < int64(len(scan.RTPMagic)) {
		t.Fatalf("unknown SSRC err=%#v", err)
	}

	trunc := filepath.Join(dir, "truncated.rtpbin")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trunc, raw[:len(raw)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = scan.BuildIndex(trunc, scan.DefaultParams())
	if !fault.IsCode(err, fault.TruncatedData) && !fault.IsCode(err, fault.CorruptHeader) {
		t.Fatalf("truncated RTP err=%v", err)
	}
}
