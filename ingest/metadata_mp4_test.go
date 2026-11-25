package ingest

import (
	"errors"
	"testing"

	"mediapack/model"
)

func TestParseMetadataAllSupportedTypesAndBadRecord(t *testing.T) {
	data := []byte(`# comment
SUB	1/1000000000	0	1000000000	hello
CHAP	1/1000	0	2000	intro
ID3	1/90000	90000	TIT2	title
EMSG	1/1000	1000	500	urn:scheme	message
SEI	1/90000	180000	5	aabb
BAD	x	y
`)
	res, err := ParseMetadata("meta", data)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[model.UnitKind]int{}
	for _, ev := range res.Events {
		counts[ev.Kind]++
		if ev.StartNanos < 0 {
			t.Fatal("negative start")
		}
	}
	if counts[model.KindSubtitle] != 1 || counts[model.KindChapter] != 1 ||
		counts[model.KindID3] != 1 || counts[model.KindEMSG] != 1 || counts[model.KindSEI] != 1 {
		t.Fatalf("missing event kinds: %+v", counts)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Category != model.CategoryInvalidArgument {
		t.Fatalf("expected one bad-record warning, got %+v", res.Warnings)
	}
}

func mp4Fixture(t *testing.T) []byte {
	t.Helper()
	samples := []MP4Sample{
		{Index: 0, PTSNanos: 0, DurationNanos: 40_000_000, Keyframe: true},
		{Index: 1, PTSNanos: 40_000_000, DurationNanos: 40_000_000, Keyframe: false},
		{Index: 2, PTSNanos: 80_000_000, DurationNanos: 40_000_000, Keyframe: true},
	}
	data, err := BuildFixtureMP4(samples)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBoxes(data); err != nil {
		t.Fatalf("go-mp4 should inspect generated boxes: %v", err)
	}
	return data
}

func TestMP4ParseNormalAndCorruptions(t *testing.T) {
	data := mp4Fixture(t)
	samples, _, err := ParseSampleBoxes("video", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 || !samples[0].Keyframe || samples[1].Keyframe {
		t.Fatalf("unexpected samples: %+v", samples)
	}

	cut, cutOffset, boxOffset, err := TruncateAt(data, len(data)*3/4)
	if err != nil {
		t.Fatal(err)
	}
	if int64(cutOffset) <= boxOffset {
		t.Fatal("cut should be inside target box")
	}
	prefix, _, perr := ParseSampleBoxes("video", cut)
	var me *model.Error
	if !errors.As(perr, &me) || me.Category != model.CategoryTruncatedBox || me.Offset != boxOffset {
		t.Fatalf("expected truncate at box %d, got %v", boxOffset, perr)
	}
	if !me.Recoverable || len(prefix) == 0 {
		t.Fatal("truncation should preserve verified prefix as recoverable")
	}

	invalid := append([]byte(nil), data...)
	off := samples[1].Offset
	invalid[off] = 0
	invalid[off+1] = 0
	invalid[off+2] = 0
	invalid[off+3] = 4
	_, _, err = ParseSampleBoxes("video", invalid)
	if !model.IsCategory(err, model.CategoryInvalidLength) {
		t.Fatalf("expected invalid_length, got %v", err)
	}

	crcBad := append([]byte(nil), data...)
	crcBad[samples[2].Offset+32] ^= 0xff
	skipped, warnings, err := ParseSampleBoxes("video", crcBad)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 2 || len(warnings) != 1 || warnings[0].Category != model.CategoryIntegrityMismatch {
		t.Fatalf("bad CRC unit should be skipped: samples=%d warnings=%+v", len(skipped), warnings)
	}

	missingKF := append([]byte(nil), data...)
	missingKF[samples[0].Offset+9] &^= 1
	parsed, _, err := ParseSampleBoxes("video", missingKF)
	if err != nil {
		t.Fatal(err)
	}
	if parsed[0].Keyframe {
		t.Fatal("keyframe mutation not observed")
	}
}
