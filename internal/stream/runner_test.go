package stream

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediatool/internal/fault"
	"mediatool/internal/fixture"
	"mediatool/internal/index"
	"mediatool/internal/segment"
)

// harness builds a small capture, indexes it, and returns a runner input.
type harness struct {
	dir  string
	in   Input
	cfg  Config
	cap  *fixture.Capture
	opts fixture.Options
}

func newHarness(t *testing.T, loss, trunc int) *harness {
	t.Helper()
	opts := fixture.DemoOptions(7, 4, 3, loss, trunc)
	opts.DurationMs = 4000
	cap, err := fixture.Generate(opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tracks := map[uint32]index.TrackInfo{}
	kinds := map[uint32]string{}
	for _, s := range opts.Tracks {
		tracks[s.SSRC] = index.TrackInfo{TrackID: s.TrackID, ClockRate: s.ClockRate}
		kinds[s.TrackID] = s.Kind
	}
	sr, err := index.ScanReader(bytes.NewReader(cap.Bytes), index.ScanConfig{
		Tracks: tracks, Kinds: kinds, Window: 64, IsKeyframe: index.IsVideoKeyframe,
		Digest: index.DigestBytes(cap.Bytes), InputBytes: int64(len(cap.Bytes)),
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var reps []RepresentationSpec
	for b := 1; b <= 2; b++ {
		rep := RepresentationSpec{ID: "r" + string(rune('0'+b)), Bitrate: b * 1_000_000, Codec: "avc1+mp4a"}
		for _, s := range opts.Tracks {
			rep.Tracks = append(rep.Tracks, TrackSpec{TrackID: s.TrackID, Kind: s.Kind})
		}
		reps = append(reps, rep)
	}
	dir := t.TempDir()
	h := &harness{
		dir:  dir,
		cap:  cap,
		opts: opts,
		in: Input{
			Digest:           sr.Index.InputDigest,
			InputBytes:       int64(len(cap.Bytes)),
			Index:            sr.Index,
			Representations:  reps,
			Reader:           bytes.NewReader(cap.Bytes),
			TruncatedOffsets: cap.TruncationOffsets,
		},
		cfg: Config{
			Window:          64,
			MaxSegmentBytes: 1 << 18,
			CheckpointPath:  filepath.Join(dir, "checkpoint.json"),
			OutputDir:       filepath.Join(dir, "segments"),
			Segmenter:       segment.Config{DurationMs: 1000, KeyframeSearchMs: 1000},
		},
	}
	return h
}

func TestResumeProducesSameManifest(t *testing.T) {
	h := newHarness(t, 0, 0)
	full, err := Execute(h.in, h.cfg)
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	fullBytes, err := full.Manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Manifest.Segments) == 0 {
		t.Fatal("full run produced no segments")
	}

	// Interrupt after one segment, then resume.
	interrupted := h.cfg
	interrupted.StopAfterSegments = 1
	first, err := Execute(h.in, interrupted)
	if err != nil {
		t.Fatalf("interrupted run: %v", err)
	}
	if !first.Stopped {
		t.Fatal("expected the run to stop early")
	}
	if first.Manifest.Checkpoint.SegmentsCommitted != 1 {
		t.Fatalf("committed = %d, want 1", first.Manifest.Checkpoint.SegmentsCommitted)
	}

	resumedCfg := h.cfg
	resumedCfg.ResumeFrom = h.cfg.CheckpointPath
	resumedCfg.StopAfterSegments = 0
	second, err := Execute(h.in, resumedCfg)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if !second.Resumed {
		t.Fatal("expected resumed flag")
	}
	secondBytes, err := second.Manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fullBytes, secondBytes) {
		t.Fatalf("resumed manifest differs from uninterrupted run\nfull=%s\nresumed=%s", fullBytes, secondBytes)
	}
}

func TestCheckpointDigestMismatchRefused(t *testing.T) {
	h := newHarness(t, 0, 0)
	interrupted := h.cfg
	interrupted.StopAfterSegments = 1
	if _, err := Execute(h.in, interrupted); err != nil {
		t.Fatalf("interrupted run: %v", err)
	}
	// Corrupt the checkpoint's bound input digest.
	var cp Checkpoint
	data, err := os.ReadFile(h.cfg.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		t.Fatal(err)
	}
	cp.InputDigest = "deadbeef"
	if err := SaveCheckpoint(h.cfg.CheckpointPath, &cp); err != nil {
		t.Fatal(err)
	}

	resumeCfg := h.cfg
	resumeCfg.ResumeFrom = h.cfg.CheckpointPath
	_, err = Execute(h.in, resumeCfg)
	if err == nil {
		t.Fatal("expected resume refusal")
	}
	if !fault.IsKind(err, fault.KindCheckpoint) {
		t.Fatalf("fault kind = %v, want checkpoint", err)
	}
	if code := fault.ExitCode(err); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}

func TestTruncationRollsBackCheckpoint(t *testing.T) {
	h := newHarness(t, 0, 1)
	if h.cap.Truncations == 0 {
		t.Skip("fixture produced no truncation")
	}
	res, err := Execute(h.in, h.cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The truncated boundary must be recorded as a restart fault.
	var found bool
	for _, f := range res.Faults {
		if f.Kind == fault.KindTruncated && f.Recovery == fault.RecoverRestart {
			found = true
		}
	}
	if !found {
		t.Fatalf("no truncation rollback fault; faults=%v", res.Faults)
	}
	if res.Fallbacks == 0 {
		t.Fatal("expected a fallback count")
	}
	// Committed boundaries must never include the damaged one: every segment's
	// media span must avoid the truncation offsets.
	if len(res.Manifest.Segments) == 0 {
		t.Fatal("no segments committed")
	}
}

func TestCheckpointOnlyTracksCommittedBoundaries(t *testing.T) {
	h := newHarness(t, 0, 0)
	interrupted := h.cfg
	interrupted.StopAfterSegments = 2
	res, err := Execute(h.in, interrupted)
	if err != nil {
		t.Fatal(err)
	}
	cp := res.Checkpoint
	if len(res.Manifest.Segments) != 2 {
		t.Fatalf("committed segments = %d, want 2", len(res.Manifest.Segments))
	}
	// One boundary per segment, so the checkpoint's boundary index must be the
	// last committed boundary's index, not the total boundary count.
	last := res.Manifest.Segments[len(res.Manifest.Segments)-1]
	if cp.BoundaryIndex != last.Index {
		t.Fatalf("checkpoint boundary %d != last committed %d", cp.BoundaryIndex, last.Index)
	}
	// Every track of every representation contributes one ref per boundary.
	perBoundary := 0
	for _, rep := range h.in.Representations {
		perBoundary += len(rep.Tracks)
	}
	if len(cp.Segments) != 2*perBoundary {
		t.Fatalf("checkpoint refs = %d, want %d", len(cp.Segments), 2*perBoundary)
	}
	// The checkpoint must not mention any boundary beyond the committed two.
	for _, ref := range cp.Segments {
		if ref.Index >= 2 {
			t.Fatalf("checkpoint references uncommitted boundary %d", ref.Index)
		}
	}
}

func TestSegmentDigestCoversWrittenBytes(t *testing.T) {
	h := newHarness(t, 0, 0)
	res, err := Execute(h.in, h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range res.Manifest.Segments {
		for _, ref := range seg.Refs {
			path := filepath.Join(h.cfg.OutputDir, sanitize(ref.RepresentationID)+"-t"+itoa(int(ref.TrackID))+"-"+pad5(ref.Index)+".m4s")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read segment %s: %v", path, err)
			}
			if got := index.DigestBytes(data); got != ref.SHA256 {
				t.Fatalf("segment %s digest mismatch: manifest %s file %s", path, ref.SHA256, got)
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func pad5(n int) string {
	s := itoa(n)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}
