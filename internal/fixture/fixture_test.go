package fixture

import (
	"bytes"
	"testing"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/mkv"
)

func TestValidFixtureParses(t *testing.T) {
	data := Valid(DefaultOptions())
	doc, err := mkv.Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("valid fixture must parse: %v", err)
	}
	if len(doc.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(doc.Tracks))
	}
	wantBlocks := 2 * 6 * 2 // tracks * clusters * blocksPerCluster
	if len(doc.Blocks) != wantBlocks {
		t.Fatalf("blocks = %d, want %d", len(doc.Blocks), wantBlocks)
	}
	if len(doc.Anomalies) != 0 {
		t.Fatalf("valid fixture must have no anomalies: %+v", doc.Anomalies)
	}
}

func TestTruncatedUnknownLengthLocated(t *testing.T) {
	data, wantOffset := TruncatedUnknownLength()
	_, err := mkv.Parse(bytes.NewReader(data), int64(len(data)))
	te := clock.AsTraceError(err)
	if te == nil {
		t.Fatal("expected truncation error")
	}
	if te.Code != clock.CodeTruncatedElement {
		t.Fatalf("code = %s", te.Code)
	}
	if te.Offset != wantOffset {
		t.Fatalf("offset = %d, want %d", te.Offset, wantOffset)
	}
}

func TestIllegalVINTLocated(t *testing.T) {
	data, wantOffset := IllegalVINT()
	_, err := mkv.Parse(bytes.NewReader(data), int64(len(data)))
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeIllegalVINT {
		t.Fatalf("expected illegal VINT, got %v", err)
	}
	if te.Offset != wantOffset {
		t.Fatalf("offset = %d, want %d", te.Offset, wantOffset)
	}
}

func TestTimeRegressionLocated(t *testing.T) {
	data, wantOffset := TimeRegression()
	doc, err := mkv.Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("syntax must stay valid, got %v", err)
	}
	if len(doc.Anomalies) != 1 {
		t.Fatalf("anomalies = %d", len(doc.Anomalies))
	}
	a := doc.Anomalies[0]
	if a.Code != clock.CodeTimeRegression || a.SourceOffset != wantOffset {
		t.Fatalf("anomaly = %+v want offset %d", a, wantOffset)
	}
}
