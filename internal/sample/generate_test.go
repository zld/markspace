package sample

import (
	"bytes"
	"testing"

	"archivecheck/internal/codec"
)

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := Generate(CaseOK, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(CaseOK, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Layers {
		if !bytes.Equal(a.Layers[i].Stream, b.Layers[i].Stream) {
			t.Fatalf("layer %s stream differs between runs", a.Layers[i].Name)
		}
		if len(a.Layers[i].Index.Entries) != len(b.Layers[i].Index.Entries) {
			t.Fatalf("layer %s index differs", a.Layers[i].Name)
		}
	}
	c, err := Generate(CaseOK, 24, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Layers[0].Stream, c.Layers[0].Stream) {
		t.Fatal("different seeds produced identical media")
	}
}

func TestGenerateExactTargetSize(t *testing.T) {
	for _, c := range []string{CaseOK, CaseSplitStartCode, CaseCorruptIndex, CaseKeyframeRegress, CaseBoundarySkew, CaseOBUTruncated} {
		a, err := Generate(c, 23, 7, []float64{900, 1800, 3600})
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		for _, l := range a.Layers {
			if l.Codec != codec.AV1 || c != CaseOBUTruncated {
				if int64(len(l.Stream)) != TargetBytes(l.TargetKbps, a.DurationMs) {
					t.Fatalf("%s/%s len=%d want %d", c, l.Name, len(l.Stream), TargetBytes(l.TargetKbps, a.DurationMs))
				}
			}
		}
		if len(a.Layers) != 3 {
			t.Fatalf("%s layers=%d", c, len(a.Layers))
		}
		if a.Layers[0].Codec != codec.H264 || a.Layers[1].Codec != codec.H265 || a.Layers[2].Codec != codec.AV1 {
			t.Fatalf("%s codecs=%v,%v,%v", c, a.Layers[0].Codec, a.Layers[1].Codec, a.Layers[2].Codec)
		}
	}
}

func TestGeneratedStreamsParseCleanly(t *testing.T) {
	a, err := Generate(CaseSplitStartCode, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range a.Layers {
		if l.Codec == codec.AV1 {
			p := codec.NewAV1Parser()
			for off := 0; off < len(l.Stream); off += 7 {
				end := off + 7
				if end > len(l.Stream) {
					end = len(l.Stream)
				}
				p.Feed(l.Stream[off:end])
			}
			p.Flush()
			rep := p.Report()
			if rep.Truncated != 0 || rep.Units == 0 || rep.Keyframes != len(l.Index.Entries) {
				t.Fatalf("av1 %s report=%+v index=%d", l.Name, rep, len(l.Index.Entries))
			}
			continue
		}
		p, err := codec.NewNALParser(l.Codec)
		if err != nil {
			t.Fatal(err)
		}
		for off := 0; off < len(l.Stream); off += 7 {
			end := off + 7
			if end > len(l.Stream) {
				end = len(l.Stream)
			}
			p.Feed(l.Stream[off:end])
		}
		p.Flush()
		rep := p.Report()
		if rep.Truncated != 0 || rep.Keyframes != len(l.Index.Entries) {
			t.Fatalf("%s %s report=%+v index=%d", l.Codec, l.Name, rep, len(l.Index.Entries))
		}
		if l.Name != "high" && rep.SplitStartCodes == 0 {
			t.Fatalf("%s expected a boundary-split start code probe", l.Name)
		}
	}
}

func TestCorruptIndexIsOutOfRange(t *testing.T) {
	a, err := Generate(CaseCorruptIndex, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	mid := a.Layers[1]
	last := mid.Index.Entries[2].Offset
	if last <= int64(len(mid.Stream)) {
		t.Fatalf("corrupt index entry is not out of range: %d <= %d", last, len(mid.Stream))
	}
}

func TestKeyframeRegressMovesBackwards(t *testing.T) {
	a, err := Generate(CaseKeyframeRegress, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	e := a.Layers[0].Index.Entries
	if e[2].PTS >= e[1].PTS {
		t.Fatalf("no regression: %d >= %d", e[2].PTS, e[1].PTS)
	}
}

func TestBoundarySkewShiftsOnlyMid(t *testing.T) {
	a, err := Generate(CaseBoundarySkew, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	if a.Layers[1].Index.Entries[1].PTS != 250*60+int64(8)*60 {
		t.Fatalf("mid pts=%d", a.Layers[1].Index.Entries[1].PTS)
	}
	if a.Layers[0].Index.Entries[1].PTS != 250*30 {
		t.Fatalf("low pts=%d", a.Layers[0].Index.Entries[1].PTS)
	}
}

func TestOBUTruncatedCutsAV1Only(t *testing.T) {
	a, err := Generate(CaseOBUTruncated, 23, 7, []float64{900, 1800, 3600})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(a.Layers[2].Stream)) >= TargetBytes(a.Layers[2].TargetKbps, a.DurationMs) {
		t.Fatal("AV1 stream was not truncated")
	}
	if int64(len(a.Layers[0].Stream)) != TargetBytes(a.Layers[0].TargetKbps, a.DurationMs) {
		t.Fatal("truncation leaked into the H.264 layer")
	}
}

func TestValidation(t *testing.T) {
	if err := ValidateCase("nope"); err == nil {
		t.Fatal("unknown case accepted")
	}
	if _, err := Generate(CaseOK, 23, 0, []float64{900, 1800, 3600}); err == nil {
		t.Fatal("zero chunk accepted")
	}
	if _, err := Generate(CaseOK, 23, 7, []float64{900, 1800}); err == nil {
		t.Fatal("two targets accepted")
	}
	if _, err := Generate(CaseOK, 23, 7, []float64{900, -1, 3600}); err == nil {
		t.Fatal("negative target accepted")
	}
	if _, err := DefaultLayers(nil); err == nil {
		t.Fatal("nil targets accepted")
	}
}
