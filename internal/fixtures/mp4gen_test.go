package fixtures

import (
	"bytes"
	"testing"

	media "media.rebuild/internal/mp4"
)

func TestDefaultMP4Indexes(t *testing.T) {
	raw := DefaultMP4().Bytes()
	idx, err := media.Read(bytes.NewReader(raw), "gen.mp4", int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Errors) != 0 {
		t.Fatalf("index errors %v", idx.Errors)
	}
	if len(idx.Tracks) != 1 || !idx.Tracks[0].Valid {
		t.Fatalf("tracks %+v", idx.Tracks)
	}
	tr := idx.Tracks[0]
	if len(tr.Samples) != 6 {
		t.Fatalf("samples %d", len(tr.Samples))
	}
	if tr.Handler != "vide" {
		t.Fatalf("handler %q", tr.Handler)
	}
	if tr.Samples[1].TimeNs != 40_000_000 || tr.Samples[1].DTS != 512 {
		t.Fatalf("sample 1 %+v", tr.Samples[1])
	}
	if tr.Samples[0].DurationNs != 40_000_000 {
		t.Fatalf("duration %d", tr.Samples[0].DurationNs)
	}
	if tr.Samples[5].Offset+int64(tr.Samples[5].Size) > int64(len(raw)) {
		t.Fatalf("sample outside file")
	}
}
