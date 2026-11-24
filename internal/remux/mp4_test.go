package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"tsforge/internal/fixture"
	"tsforge/internal/index"
	"tsforge/internal/ts"
)

// buildSamples re-reads a generated master and groups video/audio access
// units exactly as the pipeline does, but inline and small so the test does
// not depend on the pipeline package.
func buildRecords(t *testing.T, path string) []index.Record {
	t.Helper()
	psi := ts.NewPSI(0)
	type frame struct {
		offs  []int64
		pid   uint16
		kind  index.Kind
		key   bool
		first int64
	}
	var frames []frame
	cur := map[uint16]*frame{}
	flush := func(pid uint16) {
		if f := cur[pid]; f != nil {
			frames = append(frames, *f)
			delete(cur, pid)
		}
	}
	if _, _, err := ts.ReadAll(path, ts.DefaultConfig(), func(p *ts.Packet) error {
		psi.Feed(p)
		snap := psi.Snapshot()
		if snap == nil {
			return nil
		}
		st, ok := snap.StreamByPID(p.PID)
		if !ok || st.Kind == ts.StreamOther {
			return nil
		}
		kind := index.KindVideo
		if st.Kind == ts.StreamAudio {
			kind = index.KindAudio
		}
		if !p.PayloadStart {
			if f := cur[p.PID]; f != nil {
				f.offs = append(f.offs, p.Offset)
			}
			return nil
		}
		pes, err := ts.ParsePES(p.Payload)
		if err != nil || !pes.HasPTS {
			return nil
		}
		flush(p.PID)
		cur[p.PID] = &frame{offs: []int64{p.Offset}, pid: p.PID, kind: kind,
			key: p.Adaptation.RandomAccess, first: p.Index}
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	flush(fixture.VideoPID)
	flush(fixture.AudioPID)

	// Keep only a short keyframe-aligned window so the test stays fast.
	var recs []index.Record
	seenKey := false
	for _, f := range frames {
		if f.kind == index.KindVideo && f.key {
			seenKey = true
		}
		if !seenKey {
			continue
		}
		recs = append(recs, index.Record{
			PacketOffset: f.offs[0], SourceOffsets: f.offs, PID: f.pid, Kind: f.kind,
			Keyframe: f.key, SnapshotID: psi.Snapshot().ID, Segment: 0,
			DTS: f.first, PTS: f.first, Duration: 3000,
		})
		if len(recs) >= 24 {
			break
		}
	}
	return recs
}

func TestWriteSegmentProducesProbeableMP4(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.ts")
	if _, err := fixture.Generate(src, fixture.DefaultSpec(31)); err != nil {
		t.Fatal(err)
	}
	recs := buildRecords(t, src)
	// Fill durations from the ordered record DTS.
	for i := range recs {
		if i+1 < len(recs) && recs[i+1].DTS > recs[i].DTS {
			recs[i].Duration = recs[i+1].DTS - recs[i].DTS
		}
	}
	out := filepath.Join(root, "seg.mp4")
	seg, err := DefaultWriter().WriteSegment(SegmentSource{Number: 0, Records: recs,
		IndexDigest: "test-digest", InputPath: src}, out)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if seg.SHA256 == "" || seg.Bytes == 0 {
		t.Fatalf("segment facts incomplete: %+v", seg)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pi, err := mp4.Probe(f)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(pi.Tracks) == 0 {
		t.Fatal("no tracks in emitted MP4")
	}
	if pi.Timescale != Timescale {
		t.Errorf("timescale=%d, want %d", pi.Timescale, Timescale)
	}
	// The video track must be AVC with a sync sample table.
	var sawVideo bool
	for _, tr := range pi.Tracks {
		if tr.AVC != nil {
			sawVideo = true
		}
	}
	if !sawVideo {
		t.Fatal("no AVC track")
	}
}

func TestSegmentCarriesProvenanceUUID(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.ts")
	if _, err := fixture.Generate(src, fixture.DefaultSpec(32)); err != nil {
		t.Fatal(err)
	}
	recs := buildRecords(t, src)
	for i := range recs {
		if i+1 < len(recs) && recs[i+1].DTS > recs[i].DTS {
			recs[i].Duration = recs[i+1].DTS - recs[i].DTS
		}
	}
	out := filepath.Join(root, "seg.mp4")
	if _, err := DefaultWriter().WriteSegment(SegmentSource{Number: 0, Records: recs,
		IndexDigest: "digest-xyz", InputPath: src}, out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// go-mp4 does not register a uuid box type, so locate it with a raw
	// scan of the moov payload rather than the box extractor.
	raw, err := findRawBox(f, mp4.BoxTypeMoov(), "uuid")
	if err != nil {
		t.Fatalf("uuid scan: %v", err)
	}
	if len(raw) < 16 {
		t.Fatalf("uuid payload too short: %d", len(raw))
	}
	body := raw[16:]
	if !contains(body, []byte("digest-xyz")) {
		t.Fatalf("uuid payload does not cite the index digest: %s", body)
	}
}

func contains(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}

func TestReadSegmentPayloadsMatchesSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.ts")
	if _, err := fixture.Generate(src, fixture.DefaultSpec(33)); err != nil {
		t.Fatal(err)
	}
	recs := buildRecords(t, src)
	for i := range recs {
		if i+1 < len(recs) && recs[i+1].DTS > recs[i].DTS {
			recs[i].Duration = recs[i+1].DTS - recs[i].DTS
		}
	}
	out := filepath.Join(root, "seg.mp4")
	seg, err := DefaultWriter().WriteSegment(SegmentSource{Number: 0, Records: recs,
		IndexDigest: "d", InputPath: src}, out)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := ResolveSamples(src, recs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadSegmentPayloads(out)
	if err != nil {
		t.Fatal(err)
	}
	// mdat lays out video then audio. Compare the leading video run.
	videoCount := 0
	for _, s := range samples {
		if s.Record.Kind == index.KindVideo {
			videoCount++
		}
	}
	if len(got) < videoCount {
		t.Fatalf("mdat has %d samples, want at least %d video", len(got), videoCount)
	}
	vi := 0
	for _, s := range samples {
		if s.Record.Kind != index.KindVideo {
			continue
		}
		want := sha256Hex(s.Data)
		if got[vi] != want {
			t.Fatalf("video sample %d: mdat %s != source %s", vi, got[vi], want)
		}
		vi++
	}
	if seg.SampleCount != len(samples) {
		t.Fatalf("segment sample count %d != %d", seg.SampleCount, len(samples))
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestWriteSegmentRefusesCrossMapping(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.ts")
	if _, err := fixture.Generate(src, fixture.DefaultSpec(34)); err != nil {
		t.Fatal(err)
	}
	recs := buildRecords(t, src)
	for i := range recs {
		recs[i].DTS = int64(i) * 3000
		recs[i].Duration = 3000
	}
	// Make a non-keyframe sample claim a different snapshot.
	for i := range recs {
		if recs[i].Kind == index.KindVideo && !recs[i].Keyframe {
			recs[i].SnapshotID = "other"
			break
		}
	}
	_, err := DefaultWriter().WriteSegment(SegmentSource{Number: 0, Records: recs,
		IndexDigest: "d", InputPath: src}, filepath.Join(root, "seg.mp4"))
	if err == nil {
		t.Fatal("cross-mapping segment accepted")
	}
}

func TestWriteSegmentRequiresKeyframeOpen(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.ts")
	if _, err := fixture.Generate(src, fixture.DefaultSpec(35)); err != nil {
		t.Fatal(err)
	}
	recs := buildRecords(t, src)
	for i := range recs {
		recs[i].DTS = int64(i) * 3000
		recs[i].Duration = 3000
	}
	// Find the first non-keyframe video sample and make it the only video.
	var only []index.Record
	for _, r := range recs {
		if r.Kind == index.KindVideo && !r.Keyframe {
			only = append(only, r)
			break
		}
	}
	if len(only) == 0 {
		t.Skip("no non-keyframe sample available")
	}
	_, err := DefaultWriter().WriteSegment(SegmentSource{Number: 0, Records: only,
		IndexDigest: "d", InputPath: src}, filepath.Join(root, "seg.mp4"))
	if err == nil {
		t.Fatal("segment opening on a non-keyframe accepted")
	}
}
