package sample_test

import (
	"testing"

	"mediacheck/internal/sample"
)

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := sample.Generate(sample.CaseNormal, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sample.Generate(sample.CaseNormal, 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.EBML) != string(b.EBML) {
		t.Fatal("EBML bytes differ across repeated generation")
	}
	if a.Digest != b.Digest {
		t.Fatalf("digests differ: %s vs %s", a.Digest, b.Digest)
	}
	if want := "9a3edee3a95f0c684da1cab2f9d7d981858362c9b6b57760346f866d992c61ed"; a.Digest != want {
		t.Fatalf("digest=%s want stable %s", a.Digest, want)
	}
}

func TestRTPGapRemovesExactlyOnePacket(t *testing.T) {
	normal, err := sample.Generate(sample.CaseNormal, 7)
	if err != nil {
		t.Fatal(err)
	}
	gap, err := sample.Generate(sample.CaseRTPGap, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(gap.RTP) != len(normal.RTP)-1 {
		t.Fatalf("gap packets=%d normal=%d want one fewer", len(gap.RTP), len(normal.RTP))
	}
}

func TestAuxiliaryMP4ParserChecksGeneratedBox(t *testing.T) {
	box := sample.AuxiliaryMP4FileTypeBox()
	info, err := sample.InspectFirstMP4Box(box)
	if err != nil {
		t.Fatal(err)
	}
	if string(info.Type[:]) != "ftyp" {
		t.Fatalf("box type=%q want ftyp", string(info.Type[:]))
	}
	if info.Size != 20 || info.HeaderSize != 8 || info.Offset != 0 {
		t.Fatalf("unexpected box info: %+v", info)
	}
}
