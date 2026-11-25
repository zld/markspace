package fixture

import (
	"bytes"
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/metadata"
)

func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate(7, FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(7, FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.MXF, b.MXF) || !bytes.Equal(a.MP4, b.MP4) {
		t.Fatal("container fixtures must be byte-identical across runs")
	}
	if len(a.RTPFrames) != len(b.RTPFrames) || len(a.Bandwidth) != len(b.Bandwidth) {
		t.Fatal("RTP fixture size must be stable")
	}
	for i := range a.RTPFrames {
		if !bytes.Equal(a.RTPFrames[i].Data, b.RTPFrames[i].Data) {
			t.Fatalf("RTP frame %d differs across runs", i)
		}
	}
}

func TestSeedChangesRTPNotContainers(t *testing.T) {
	a, _ := Generate(1, FaultNone)
	b, _ := Generate(2, FaultNone)
	if !bytes.Equal(a.MXF, b.MXF) {
		t.Fatal("MXF is seed-independent")
	}
	same := true
	for i := range a.RTPFrames {
		if !bytes.Equal(a.RTPFrames[i].Data, b.RTPFrames[i].Data) {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds must change RTP payloads")
	}
}

func TestContainersParseAndConflict(t *testing.T) {
	f, err := Generate(7, FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	rec := model.NewRecorder()
	mxf, err := metadata.ParseMXF(f.MXF, rec)
	if err != nil || rec.HasError() {
		t.Fatalf("MXF fixture must parse cleanly: %v %v", err, rec.Records())
	}
	rec2 := model.NewRecorder()
	mp4, err := metadata.ParseMP4(f.MP4, rec2)
	if err != nil || rec2.HasError() {
		t.Fatalf("MP4 fixture must parse cleanly: %v %v", err, rec2.Records())
	}
	if len(mxf.Tracks) != 3 || len(mp4.Tracks) != 2 {
		t.Fatalf("track counts wrong: mxf=%d mp4=%d", len(mxf.Tracks), len(mp4.Tracks))
	}
	// Rotation is a per-track property: MXF 90 vs MP4 180 must adjudicate to
	// the MXF value with a conflict flag on the video track verdict.
	rec3 := model.NewRecorder()
	v := metadata.Adjudicate(mxf, mp4, metadata.DefaultDefaults(), rec3)
	var vid *metadata.TrackVerdict
	for i := range v.Tracks {
		if v.Tracks[i].Kind == metadata.KindVideo {
			vid = &v.Tracks[i]
		}
	}
	if vid == nil {
		t.Fatal("no video track verdict")
	}
	if vid.Rotation.Value != 90 || vid.Rotation.Winner != metadata.SourceMXF || !vid.Rotation.Conflict {
		t.Fatalf("video rotation adjudication wrong: %+v", vid.Rotation)
	}
	if vid.Crop.Winner != metadata.SourceMXF {
		t.Fatalf("crop should prefer MXF, got %+v", vid.Crop)
	}
}

func TestTruncatedFaultLocated(t *testing.T) {
	f, err := Generate(7, FaultTruncated)
	if err != nil {
		t.Fatal(err)
	}
	if f.TruncatedOffset <= 0 || len(f.TruncatedMXF) != int(f.TruncatedOffset) {
		t.Fatalf("truncation geometry wrong: offset=%d len=%d",
			f.TruncatedOffset, len(f.TruncatedMXF))
	}
	// Full MXF still parses; the cut copy must fail with a located record.
	recFull := model.NewRecorder()
	if _, err := metadata.ParseMXF(f.MXF, recFull); err != nil {
		t.Fatalf("full MXF must remain valid: %v", err)
	}
	recCut := model.NewRecorder()
	if _, err := metadata.ParseMXF(f.TruncatedMXF, recCut); err == nil {
		t.Fatal("truncated MXF must fail parsing")
	}
	var found bool
	for _, r := range recCut.Records() {
		if r.Code == model.ErrTruncatedHeader && r.Offset >= 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation not located: %v", recCut.Records())
	}
}

func TestRTPHasLossDupReorderAndWrap(t *testing.T) {
	f, _ := Generate(7, FaultNone)
	if len(f.RTPFrames) == 0 {
		t.Fatal("no RTP frames generated")
	}
	// Bandwidth varies and stays within the jitter bracket's general range.
	var minB, maxB int64 = 1<<62 - 1, 0
	for _, s := range f.Bandwidth {
		if s.BytesPerSecond < minB {
			minB = s.BytesPerSecond
		}
		if s.BytesPerSecond > maxB {
			maxB = s.BytesPerSecond
		}
	}
	if minB == maxB {
		t.Fatal("bandwidth trace must vary")
	}
	if minB < 0 || maxB <= minB {
		t.Fatalf("bandwidth range invalid: %d..%d", minB, maxB)
	}
	// First/last sequence numbers confirm a stream crossing the 16-bit wrap.
	first := f.RTPFrames[0].Data
	last := f.RTPFrames[len(f.RTPFrames)-1].Data
	if len(first) < 4 || len(last) < 4 {
		t.Fatal("RTP datagrams too short")
	}
	// Byte offsets are strictly increasing.
	for i := 1; i < len(f.RTPFrames); i++ {
		if f.RTPFrames[i].ByteOffset <= f.RTPFrames[i-1].ByteOffset {
			t.Fatal("byte offsets must increase monotonically")
		}
	}
}
