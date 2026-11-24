package parse

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"

	"mediarun/internal/fault"
)

// buildSegment creates one media segment with n samples of dur units and
// size bytes each, starting at baseDecodeTime. Every k-th sample is sync.
func buildSegment(t *testing.T, seq uint32, base uint64, n int, dur, size uint32, syncEvery int) []byte {
	t.Helper()
	frag, err := mp4.CreateFragment(seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		flags := mp4.NonSyncSampleFlags
		if syncEvery > 0 && i%syncEvery == 0 {
			flags = mp4.SyncSampleFlags
		}
		frag.AddFullSample(mp4.FullSample{
			Sample:     mp4.NewSample(flags, dur, size, 0),
			DecodeTime: base + uint64(i)*uint64(dur),
			Data:       bytes.Repeat([]byte{byte(i)}, int(size)),
		})
	}
	var buf bytes.Buffer
	if err := frag.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseMediaSegmentGood(t *testing.T) {
	data := buildSegment(t, 7, 180000, 10, 3000, 100, 5)
	seg, err := ParseMediaSegment("seg-7.m4s", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Fragments) != 1 {
		t.Fatalf("got %d fragments", len(seg.Fragments))
	}
	f := seg.Fragments[0]
	if f.SeqNumber != 7 || f.TrackID != 1 || f.BaseDecodeTime != 180000 {
		t.Fatalf("bad fragment: %+v", f)
	}
	if len(f.Samples) != 10 {
		t.Fatalf("got %d samples", len(f.Samples))
	}
	for i, s := range f.Samples {
		if s.DecodeTime != 180000+uint64(i)*3000 {
			t.Fatalf("sample %d decode time %d", i, s.DecodeTime)
		}
		if s.DataOffset <= 0 || s.DataOffset >= int64(len(data)) {
			t.Fatalf("sample %d bad data offset %d", i, s.DataOffset)
		}
		if i > 0 && f.Samples[i].DataOffset != f.Samples[i-1].DataOffset+100 {
			t.Fatalf("sample %d offset not contiguous", i)
		}
	}
	if !f.Samples[0].Sync || !f.Samples[5].Sync || f.Samples[1].Sync {
		t.Fatalf("bad sync flags")
	}
	if seg.MediaBytes != 1000 {
		t.Fatalf("media bytes = %d", seg.MediaBytes)
	}
}

func faultStage(t *testing.T, err error) fault.Stage {
	t.Helper()
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("want fault.Error, got %T: %v", err, err)
	}
	return fe.Stage
}

func TestParseTruncatedBoxHeader(t *testing.T) {
	data := buildSegment(t, 1, 0, 4, 3000, 50, 1)
	data = append(data, 0, 0, 0, 10) // half a box header
	_, err := ParseMediaSegment("x.m4s", data)
	if got := faultStage(t, err); got != fault.StageBoxHeader {
		t.Fatalf("stage = %s", got)
	}
	var fe *fault.Error
	errors.As(err, &fe)
	if fe.Offset != int64(len(data)-4) {
		t.Fatalf("offset = %d, want %d", fe.Offset, len(data)-4)
	}
}

func TestParseTruncatedSampleTable(t *testing.T) {
	data := buildSegment(t, 1, 0, 4, 3000, 50, 1)
	// Cut inside the moof (which precedes mdat).
	cut := len(data) / 3
	_, err := ParseMediaSegment("x.m4s", data[:cut])
	if got := faultStage(t, err); got != fault.StageSampleTable {
		t.Fatalf("stage = %s", got)
	}
	var fe *fault.Error
	errors.As(err, &fe)
	if fe.Offset != int64(cut) {
		t.Fatalf("offset = %d, want %d", fe.Offset, cut)
	}
}

func TestParseTruncatedMediaPayload(t *testing.T) {
	data := buildSegment(t, 1, 0, 4, 3000, 50, 1)
	// Keep the whole moof, cut inside mdat payload.
	cut := len(data) - 20
	_, err := ParseMediaSegment("x.m4s", data[:cut])
	if got := faultStage(t, err); got != fault.StageMediaPayload {
		t.Fatalf("stage = %s", got)
	}
}

func TestParseInit(t *testing.T) {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(90000, "video", "und")
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	tracks, err := ParseInit("init.mp4", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[1].Timescale != 90000 {
		t.Fatalf("bad tracks: %+v", tracks)
	}
}

func TestParseInitZeroTimescale(t *testing.T) {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(0, "video", "und")
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseInit("init.mp4", buf.Bytes())
	if got := faultStage(t, err); got != fault.StageSampleTable {
		t.Fatalf("stage = %s", got)
	}
}
