package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/audio"
	"rtpreplay/fingerprint"
	"rtpreplay/ingest"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
	"rtpreplay/pipeline"
	"rtpreplay/segment"
)

func sampleReps(t *testing.T) []*pipeline.Representation {
	t.Helper()
	mk := func(kbps int) *ingest.Stream {
		var buf bytes.Buffer
		_ = rtplog.WriteHeader(&buf)
		base := time.Unix(1_700_000_000, 0).UTC()
		for i := 0; i < 8; i++ {
			p := rtp.Packet{
				Header: rtp.Header{
					Version: 2, PayloadType: 8,
					SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 1,
				},
				Payload: []byte{0, 0},
			}
			raw, _ := p.Marshal()
			_ = rtplog.WriteRecord(&buf, base.Add(time.Duration(i)*20*time.Millisecond), raw)
		}
		st, err := ingest.ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()),
			media.AudioClockRate, kbps)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	reps, err := pipeline.Fanout([]*ingest.Stream{mk(16), mk(32)}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		r.LocateBoundaries(40)
	}
	return reps
}

func TestBuildSummaryFields(t *testing.T) {
	reps := sampleReps(t)
	sync := pipeline.FanIn(reps)
	var segs []segment.Segment
	for _, r := range reps {
		segs = append(segs, segment.Split(r)...)
	}
	silence := audio.DetectSilence(reps[0], audio.DefaultLevelThreshold)
	dups := fingerprint.Dedupe(reps[0], 1)

	s := Build(reps, sync, segs, silence, dups)
	if s.SyncErrorMS != 0 || s.SyncFailed {
		t.Fatalf("aligned reps must sync: %+v", s)
	}
	if s.LossRecoveryRate != 1 || s.LatencyP95MS != 0 {
		t.Fatalf("pristine metrics wrong: rate=%f p95=%f", s.LossRecoveryRate, s.LatencyP95MS)
	}
	if len(s.Segments) == 0 || len(s.SilenceRegions) == 0 {
		t.Fatalf("segments/silence missing: %+v", s)
	}
	if _, ok := s.DriftPPM["audio@16kbps"]; !ok {
		t.Fatalf("drift map missing key: %+v", s.DriftPPM)
	}
	// Every required JSON key must serialize.
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{
		"sync_error_ms", "loss_recovery_rate", "latency_p95_ms",
		"segments", "silence_regions", "duplicates",
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("JSON missing %s: %s", key, text)
		}
	}
}

func TestDeterministicReruns(t *testing.T) {
	build := func() string {
		reps := sampleReps(t)
		sync := pipeline.FanIn(reps)
		var segs []segment.Segment
		for _, r := range reps {
			segs = append(segs, segment.Split(r)...)
		}
		s := Build(reps, sync, segs,
			audio.DetectSilence(reps[0], audio.DefaultLevelThreshold),
			fingerprint.Dedupe(reps[0], 1))
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	if build() != build() {
		t.Fatal("summary must be byte-identical across reruns")
	}
}

func TestAttachError(t *testing.T) {
	s := Build(nil, nil, nil, nil, nil)
	err := media.NewError(media.KindBadInput, "TRUNCATED_BOX", "mp4", 123, "cut short")
	s.AttachError(err)
	if s.ErrorCode != "TRUNCATED_BOX" || s.ErrorOffset == nil || *s.ErrorOffset != 123 {
		t.Fatalf("error attachment wrong: %+v", s)
	}
}

func TestWriteJSONRefusesInputDir(t *testing.T) {
	root := t.TempDir()
	inputDir := filepath.Join(root, "input")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Build(nil, nil, nil, nil, nil)
	inside := filepath.Join(inputDir, "report.json")
	err := s.WriteJSON(inside, inputDir)
	le, ok := media.AsLocatable(err)
	if !ok || le.Code != "OUTPUT_IN_INPUT_DIR" {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(inside); !os.IsNotExist(statErr) {
		t.Fatal("no file may be created inside the input directory")
	}

	// A sibling directory is accepted.
	outside := filepath.Join(root, "out", "report.json")
	if err := s.WriteJSON(outside, inputDir); err != nil {
		t.Fatalf("outside write must succeed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("report not written: %v", err)
	}
}
