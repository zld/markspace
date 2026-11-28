package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediad/fixture"
	"mediad/timeline"
)

// buildTestInputs creates two video renditions at 30 fps with a keyframe every
// second, plus a subtitle track carrying two cues, all as in-memory inputs.
func buildTestInputs(t *testing.T) []Input {
	t.Helper()
	mk := func(id uint32, name string, width uint16) Input {
		n := 90 // 3 s at 30 fps
		var samples []fixture.Sample
		for i := 0; i < n; i++ {
			samples = append(samples, fixture.Sample{
				Data:          []byte{byte(id), byte(i & 0xff), 0x11, 0x22},
				DurationTicks: 3000,
				Sync:          i%30 == 0,
			})
		}
		clip := fixture.BuildClip([]fixture.TrackSpec{
			{ID: id, Handler: "vide", Timescale: 90000, Width: width, Height: 9 * width / 16, Samples: samples},
			{ID: id + 10, Handler: "subt", Timescale: 90000, Samples: []fixture.Sample{
				{Data: []byte{0, 5, 'H', 'e', 'l', 'l', 'o'}, DurationTicks: 90000, Sync: true},
				{Data: []byte{0, 5, 'E', 'n', 'd', '!', '!'}, DurationTicks: 90000, Sync: true},
			}},
		})
		return Input{Path: name, Bytes: clip.Bytes()}
	}
	return []Input{
		mk(1, name("r360p.mp4"), 640),
		mk(2, name("r720p.mp4"), 1280),
	}
}

func name(p string) string { return filepath.Join("/virtual", p) }

func newTestJob(t *testing.T, dir string) *Job {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ChunkSize = 1
	cfg.SegmentTargetUs = 1_000_000
	job, err := NewJob(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestRunProducesCompleteAlignedManifest(t *testing.T) {
	dir := t.TempDir()
	job := newTestJob(t, dir)
	res, err := job.Run(buildTestInputs(t), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest == nil || res.Output == nil {
		t.Fatal("expected a published manifest")
	}
	if res.Manifest.Stats.SegmentCount != 3 {
		t.Fatalf("segments = %d", res.Manifest.Stats.SegmentCount)
	}
	if res.Manifest.Stats.AlignedSegments != 3 {
		t.Fatalf("aligned = %d", res.Manifest.Stats.AlignedSegments)
	}
	if res.Manifest.Stats.EventCount != 2 {
		t.Fatalf("events = %d", res.Manifest.Stats.EventCount)
	}
	if res.Manifest.Digest == "" {
		t.Fatal("manifest digest missing")
	}
	// Every segment must align both renditions.
	for _, s := range res.Manifest.Segments {
		if len(s.Renditions) != 2 {
			t.Fatalf("segment %d has %d renditions", s.Index, len(s.Renditions))
		}
	}
}

func TestRunResumeMatchesContinuousDigest(t *testing.T) {
	// Continuous run.
	contJob := newTestJob(t, t.TempDir())
	cont, err := contJob.Run(buildTestInputs(t), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	contDigest := cont.Manifest.Digest

	// Run that stops after 2 chunks, then resumes in a fresh Job sharing dir.
	dir := t.TempDir()
	first := newTestJob(t, dir)
	partial, err := first.Run(buildTestInputs(t), RunOptions{StopAfterChunks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !partial.Partial || partial.Output != nil {
		t.Fatal("stop-after must return a partial result without output")
	}
	if partial.Manifest != nil && partial.Manifest.Complete {
		t.Fatal("partial manifest must not be complete")
	}

	// Same Job continues (simulating a restarted process reading the checkpoint).
	second := newTestJob(t, dir)
	resumed, err := second.Run(buildTestInputs(t), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ResumedFromChunk < 1 {
		t.Fatalf("resume should find committed chunks, got %d", resumed.ResumedFromChunk)
	}
	if resumed.Manifest.Digest != contDigest {
		t.Fatalf("resumed digest %s != continuous %s", resumed.Manifest.Digest, contDigest)
	}
}

func TestRunRefusesDamagedInput(t *testing.T) {
	dir := t.TempDir()
	job := newTestJob(t, dir)
	inputs := buildTestInputs(t)
	// Truncate the second rendition so its sample offsets run past EOF.
	inputs[1].Bytes = inputs[1].Bytes[:len(inputs[1].Bytes)/3]
	_, err := job.Run(inputs, RunOptions{})
	if err == nil {
		t.Fatal("a damaged input must make the run fail")
	}
	if timeline.CodeOf(err) != timeline.CodeUnsupported && timeline.CodeOf(err) != timeline.CodeManifest && timeline.CodeOf(err) != timeline.CodeRefused {
		t.Logf("error code = %q (err=%v)", timeline.CodeOf(err), err)
	}
}

func TestManifestRefusesIncompleteOrFatal(t *testing.T) {
	m := &Manifest{Schema: 1, Complete: false}
	if _, err := m.Publish(); err == nil {
		t.Fatal("incomplete manifest must refuse to publish")
	}
	m.Complete = true
	m.Issues = []timeline.Issue{{Severity: timeline.SeverityError, Code: timeline.CodeTruncated, Message: "x"}}
	if _, err := m.Publish(); err == nil {
		t.Fatal("manifest with fatal issues must refuse to publish")
	}
}

func TestConfigSumChangesWithParameters(t *testing.T) {
	a := DefaultConfig()
	b := a
	if a.Sum() != b.Sum() {
		t.Fatal("equal configs must share a sum")
	}
	b.ChunkSize = a.ChunkSize + 1
	if a.Sum() == b.Sum() {
		t.Fatal("chunk size must change the config sum")
	}
}

func TestPercentileNearestRank(t *testing.T) {
	samples := []float64{5, 1, 3, 2, 4}
	if got := Percentile(samples, 95); got != 5 {
		t.Fatalf("p95 = %v", got)
	}
	if got := Percentile(samples, 0); got != 1 {
		t.Fatalf("p0 = %v", got)
	}
	if got := Percentile(nil, 95); got != 0 {
		t.Fatalf("empty p95 = %v", got)
	}
}

func TestChunkSegmentsFixedSize(t *testing.T) {
	var segs []SegmentInfo
	for i := 0; i < 7; i++ {
		segs = append(segs, SegmentInfo{Index: i})
	}
	chunks := chunkSegments(segs, 3)
	if len(chunks) != 3 || len(chunks[0].segments) != 3 || len(chunks[2].segments) != 1 {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestRTPInputFeedsLossRecovery(t *testing.T) {
	dir := t.TempDir()
	job := newTestJob(t, dir)
	// Capture with a duplicate and a reorder but no loss.
	var dgrams []fixture.RTPDatagram
	for i := 0; i < 100; i++ {
		seq := uint16(i + 1)
		pkt := &rtp.Packet{
			Header:  rtp.Header{Version: 2, SSRC: 9, SequenceNumber: seq, Timestamp: uint32(i * 3000), Marker: i%10 == 9},
			Payload: []byte("payload"),
		}
		b, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		dgrams = append(dgrams, b)
	}
	// Duplicate packet 5 and replay it later.
	dgrams = append(dgrams, dgrams[4])
	capture := fixture.EncodeRTPCapture(dgrams)
	in := Input{Path: name("stream.rtp"), Bytes: capture, IsRTP: true}
	res, err := job.ScanInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.RTP == nil {
		t.Fatal("RTP recovery result missing")
	}
	if res.RTP.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", res.RTP.Duplicates)
	}
	if res.RTP.LossRecoveryRate() != 1 {
		t.Fatalf("loss rate = %v, want 1 (duplicate is not loss)", res.RTP.LossRecoveryRate())
	}
}

func TestRTPLossIsReported(t *testing.T) {
	dir := t.TempDir()
	job := newTestJob(t, dir)
	var dgrams []fixture.RTPDatagram
	for i := 0; i < 200; i++ {
		if i == 5 {
			continue // drop one packet, well inside the stream
		}
		pkt := &rtp.Packet{
			Header:  rtp.Header{Version: 2, SSRC: 9, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 3000)},
			Payload: []byte("p"),
		}
		b, _ := pkt.Marshal()
		dgrams = append(dgrams, b)
	}
	res, err := job.ScanInput(Input{Path: name("loss.rtp"), Bytes: fixture.EncodeRTPCapture(dgrams), IsRTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RTP.Unrecoverable != 1 {
		t.Fatalf("loss not counted: %+v", res.RTP)
	}
	// Unrecoverable loss is recorded as a warning; the pipeline decides whether
	// its rate breaches the budget and must block publication.
	if len(res.Issues) == 0 {
		t.Fatal("unrecoverable loss must be recorded as an issue")
	}
	found := false
	for _, is := range res.Issues {
		if is.Code == timeline.CodeUnrecoverable && is.Severity == timeline.SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unrecoverable_loss warning, got %v", res.Issues.Strings())
	}
	if rate := res.RTP.LossRecoveryRate(); rate <= 0.99 {
		t.Fatalf("loss rate = %v", rate)
	}
}

func TestScanReusesIndexUntilSamePathReplacement(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.SegmentTargetUs = 1_000_000
	job, err := NewJob(cfg, filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	first := fixture.BuildDemo(fixture.DemoOptions{Seed: 7, Loss: 0.04}).Files[0]
	path := filepath.Join(dir, "rendition.mp4")
	if err := os.WriteFile(path, first.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	r1, err := job.ScanInput(Input{Path: path, Bytes: first.Bytes})
	if err != nil {
		t.Fatal(err)
	}
	if r1.IndexReused {
		t.Fatal("a first scan cannot reuse an index")
	}
	// Re-scanning unchanged bytes reuses the stored index.
	r2, _ := job.ScanInput(Input{Path: path, Bytes: first.Bytes})
	if !r2.IndexReused {
		t.Fatal("an unchanged input should reuse its index")
	}
	// Replacing the bytes at the same path must defeat reuse.
	second := fixture.BuildDemo(fixture.DemoOptions{Seed: 99, Loss: 0.04}).Files[0]
	if err := os.WriteFile(path, second.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	r3, _ := job.ScanInput(Input{Path: path, Bytes: second.Bytes})
	if r3.IndexReused {
		t.Fatal("a same-path replacement must not reuse the old index")
	}
}

func TestRunBlocksWhenLossBudgetBreached(t *testing.T) {
	// A healthy video rendition plus an RTP stream with five permanent drops in
	// 100 packets. With a 99.9% recovery requirement the run must fail on the
	// loss budget alone; without enforcement it would publish.
	cfg := DefaultConfig()
	cfg.SegmentTargetUs = 1_000_000
	cfg.LossBudget = 0.001
	video := buildTestInputs(t)[0] // one rendition, enough to align

	var dgrams []fixture.RTPDatagram
	for i := 0; i < 100; i++ {
		if i == 10 || i == 20 || i == 30 || i == 40 || i == 50 {
			continue
		}
		pkt := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 7, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 3000)}}
		b, _ := pkt.Marshal()
		dgrams = append(dgrams, b)
	}
	rtpIn := Input{Path: name("budget.rtp"), Bytes: fixture.EncodeRTPCapture(dgrams), IsRTP: true}

	job, err := NewJob(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, runErr := job.Run([]Input{video, rtpIn}, RunOptions{})
	if runErr == nil {
		t.Fatal("expected the run to fail on the loss budget")
	}
	if timeline.CodeOf(runErr) != timeline.CodeRefused && timeline.CodeOf(runErr) != timeline.CodeManifest && timeline.CodeOf(runErr) != timeline.CodeNoBoundary {
		t.Logf("refusal code = %q: %v", timeline.CodeOf(runErr), runErr)
	}
	if res == nil || !res.Issues.HasErrors() {
		t.Fatal("the budget breach must be a fatal issue")
	}
	found := false
	for _, is := range res.Issues {
		if is.Code == timeline.CodeUnrecoverable && is.Severity == timeline.SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a fatal unrecoverable_loss issue, got %v", res.Issues.Strings())
	}
}
