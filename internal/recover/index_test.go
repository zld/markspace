package recover

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/zld/mediacore/internal/trace"
)

func sampleBox(track, index int, start uint64, duration uint32, payload []byte) []byte {
	out := make([]byte, 8+SampleHeaderSize+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], "mcs ")
	copy(out[8+0:8+4], SampleMagic[:])
	binary.BigEndian.PutUint32(out[12:16], uint32(track))
	binary.BigEndian.PutUint32(out[16:20], uint32(index))
	binary.BigEndian.PutUint64(out[20:28], start)
	binary.BigEndian.PutUint32(out[28:32], duration)
	copy(out[8+SampleHeaderSize:], payload)
	return out
}

func TestScanCompleteBoxes(t *testing.T) {
	a := sampleBox(1, 0, 0, 10, []byte("a"))
	b := sampleBox(1, 1, 10, 10, []byte("bb"))
	rep, err := ScanRootBoxes(append(a, b...))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Boxes) != 2 || !rep.Boxes[0].Complete || !rep.Boxes[1].Complete {
		t.Fatalf("boxes = %+v", rep.Boxes)
	}
}

func TestScanRejectsImpossibleLengthAtBoxOffset(t *testing.T) {
	data := sampleBox(1, 0, 0, 10, []byte("payload"))
	binary.BigEndian.PutUint32(data[0:4], 4)
	_, err := ScanRootBoxes(data)
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodeBoxLengthOutOfBounds {
		t.Fatalf("error = %v", err)
	}
	if !te.HasByteOffset || te.ByteOffset != 0 {
		t.Fatalf("offset = %+v", te)
	}
}

func TestRebuildTracksFromTruncatedBox(t *testing.T) {
	first := sampleBox(1, 0, 0, 100, []byte("one"))
	second := sampleBox(1, 1, 100, 100, make([]byte, 16))
	full := append(first, second...)
	cut := int64(len(first) + 8 + SampleHeaderSize + 4)
	truncated := full[:cut]

	rep, err := ScanRootBoxes(truncated)
	if !trace.HasCode(err, trace.CodeTruncated) {
		t.Fatalf("expected truncation, got %v", err)
	}
	te, _ := trace.As(err)
	if te.ByteOffset != cut {
		t.Fatalf("failure offset = %d, want %d", te.ByteOffset, cut)
	}

	tracks, damages := RebuildTracks(truncated, rep, []TrackMeta{{ID: 1, TimeScale: 1000}})
	if len(tracks) != 1 || len(tracks[0].Samples) != 1 {
		t.Fatalf("expected only complete sample, tracks=%+v", tracks)
	}
	if len(damages) != 1 || damages[0].ByteOffset != cut {
		t.Fatalf("damages = %+v", damages)
	}
	if damages[0].Range.Start != 100_000_000 || damages[0].Range.End != 200_000_000 {
		t.Fatalf("damage range = %+v", damages[0].Range)
	}
}
