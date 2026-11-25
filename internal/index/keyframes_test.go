package index_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"mediapack/internal/index"
	"mediapack/internal/sample"
	"mediapack/internal/ts"
)

func buildRealIndex(t *testing.T) (*index.KeyframeIndex, *sample.Layout) {
	t.Helper()
	L := sample.Generate(sample.DefaultConfig())
	idx := index.New()
	rd := ts.NewReader(bytes.NewReader(L.Data), 0, nil)
	for {
		p, err := rd.Next()
		if errors.Is(err, ts.ErrTruncated) || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		idx.AddPacket(p)
	}
	return idx, L
}

func TestKeyframeIndexLocatesPacketOffsets(t *testing.T) {
	idx, L := buildRealIndex(t)
	if len(idx.Keys) != len(L.Intervals) {
		t.Fatalf("keys=%d want %d", len(idx.Keys), len(L.Intervals))
	}
	probes := make([]int64, 0, len(L.Intervals))
	for _, iv := range L.Intervals {
		probes = append(probes, iv.KeyframeOffset)
		if got := idx.Exact(iv.KeyframeOffset); got == nil {
			t.Fatalf("cannot locate keyframe at %d", iv.KeyframeOffset)
		} else if got.PacketIndex != iv.KeyframePacket {
			t.Fatalf("packet index=%d want %d", got.PacketIndex, iv.KeyframePacket)
		}
	}
	if rate := idx.HitRate(probes); rate != 1.0 {
		t.Fatalf("hit rate=%v want 1.0", rate)
	}

	// The CC-jump offset is exactly a keyframe offset, so interval mapping
	// must associate it with the right keyframe interval.
	kf, start, end := idx.IntervalFor(L.Anomalies.CCJumpOffset, int64(len(L.Data)))
	if kf == nil || kf.Offset != L.Anomalies.CCJumpOffset {
		t.Fatalf("jump not associated with keyframe: %+v", kf)
	}
	if start != L.Anomalies.CCJumpOffset || end <= start {
		t.Fatalf("bad containing interval [%d,%d)", start, end)
	}
}

func TestIsIDRRejectsNonKeyframe(t *testing.T) {
	idr := &ts.Packet{Payload: []byte{0, 0, 0, 1, 0x65}}
	non := &ts.Packet{Payload: []byte{0, 0, 1, 0x41, 0}}
	if !index.IsIDR(idr) || index.IsIDR(non) {
		t.Fatal("IDR detection wrong")
	}
}
