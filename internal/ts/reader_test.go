package ts_test

import (
	"bytes"
	"errors"
	"testing"

	merr "mediapack/internal/errors"
	"mediapack/internal/sample"
	"mediapack/internal/ts"
)

// parseFixture streams the generated fixture through the reader.
func parseFixture(t *testing.T) ([]*ts.Packet, *merr.Book) {
	t.Helper()
	L := sample.Generate(sample.DefaultConfig())
	book := merr.NewBook()
	rd := ts.NewReader(bytes.NewReader(L.Data), 0, book)
	var pkts []*ts.Packet
	for {
		p, err := rd.Next()
		if errors.Is(err, ts.ErrTruncated) {
			break
		}
		if err != nil {
			t.Fatalf("reader: %v", err)
		}
		if p == nil {
			break
		}
		pkts = append(pkts, p)
	}
	return pkts, book
}

func TestReaderReportsSyncAndTruncationOffsets(t *testing.T) {
	L := sample.Generate(sample.DefaultConfig())
	_, book := parseFixture(t)

	syncs := book.Of(merr.CatSyncByte)
	if len(syncs) != 1 {
		t.Fatalf("sync errors=%d want 1", len(syncs))
	}
	if syncs[0].Offset != L.Anomalies.SyncErrorOffset {
		t.Fatalf("sync offset=%d want %d", syncs[0].Offset, L.Anomalies.SyncErrorOffset)
	}

	truncs := book.Of(merr.CatTruncatedPacket)
	if len(truncs) != 1 {
		t.Fatalf("truncated=%d want 1", len(truncs))
	}
	if truncs[0].Offset != L.Anomalies.TruncatedTail {
		t.Fatalf("trunc offset=%d want %d", truncs[0].Offset, L.Anomalies.TruncatedTail)
	}
	if truncs[0].InputEnd <= truncs[0].InputStart {
		t.Fatal("truncated issue must carry a non-empty input range")
	}
}

func TestCCJumpAndDuplicateClassification(t *testing.T) {
	L := sample.Generate(sample.DefaultConfig())
	pkts, _ := parseFixture(t)

	tr := ts.NewCCTracker()
	var jumps, dups []ts.CCEvent
	for _, p := range pkts {
		if e := tr.Observe(p); e != nil {
			switch e.Kind {
			case ts.CCJump:
				jumps = append(jumps, *e)
			case ts.CCDuplicate:
				dups = append(dups, *e)
			}
		}
	}
	if len(jumps) != 1 {
		t.Fatalf("cc jumps=%d want 1", len(jumps))
	}
	if jumps[0].CurOffset != L.Anomalies.CCJumpOffset {
		t.Fatalf("jump offset=%d want %d", jumps[0].CurOffset, L.Anomalies.CCJumpOffset)
	}
	if jumps[0].Missing != 1 {
		t.Fatalf("missing packets=%d want 1", jumps[0].Missing)
	}
	if jumps[0].PrevOffset == 0 || jumps[0].CurOffset <= jumps[0].PrevOffset {
		t.Fatal("jump must record a gap offset range")
	}
	if len(dups) != 1 || dups[0].CurOffset != L.Anomalies.CCDuplicateOffset {
		t.Fatalf("duplicates=%+v want exactly one at %d", dups, L.Anomalies.CCDuplicateOffset)
	}
}

func TestPacketCountAndKeyframePES(t *testing.T) {
	L := sample.Generate(sample.DefaultConfig())
	pkts, _ := parseFixture(t)
	if len(pkts) != L.FullPackets {
		t.Fatalf("full packets=%d want %d", len(pkts), L.FullPackets)
	}
	kf := pkts[L.Intervals[2].KeyframePacket]
	if !kf.PUSI || kf.PES == nil || !kf.PES.HasPTS || !kf.PES.HasDTS {
		t.Fatal("wrap-boundary keyframe must carry PTS+DTS")
	}
	if !kf.PCRFlag {
		t.Fatal("keyframe must carry PCR")
	}
}
