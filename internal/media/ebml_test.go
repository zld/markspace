package media_test

import (
	"errors"
	"testing"

	"continuity/internal/fixture"
	"continuity/internal/media"
)

func TestReadVINTKnownUnknownAndMalformed(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		want    uint64
		unknown bool
		wantErr error
	}{
		{name: "one byte", data: []byte{0x81}, want: 1},
		{name: "two byte", data: []byte{0x40, 0x01}, want: 1},
		{name: "unknown one byte", data: []byte{0xff}, want: 127, unknown: true},
		{name: "zero first byte", data: []byte{0x00}, wantErr: media.ErrInvalidVINT},
		{name: "truncated continuation", data: []byte{0x01, 0xff}, wantErr: media.ErrTruncatedHeader},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, _, err := media.ReadVINT(tc.data, 0, int64(len(tc.data)), "/test")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err=%v, want %v", err, tc.wantErr)
				}
				me, ok := media.AsError(err)
				if !ok || me.Offset != 0 {
					t.Fatalf("structured offset=%+v ok=%v, want 0", me, ok)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if v.Value != tc.want || v.Unknown != tc.unknown {
				t.Fatalf("VINT=%+v want value=%d unknown=%v", v, tc.want, tc.unknown)
			}
		})
	}
}

func TestParseKnownInterleavedMKV(t *testing.T) {
	data := fixture.BuildMKV(fixture.MKVSpec{Seed: 17, Clusters: 2})
	doc, err := media.ParseEBML(data, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := media.ExtractBlocks(data, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 60 {
		t.Fatalf("blocks=%d, want 60", len(blocks))
	}
	var audio, video int
	for _, b := range blocks {
		switch b.TrackID {
		case 1:
			video++
		case 2:
			audio++
		default:
			t.Fatalf("unexpected track %d", b.TrackID)
		}
		if b.ElementOffset <= 0 || b.PTSNS < 0 {
			t.Fatalf("invalid block %+v", b)
		}
	}
	if audio != 40 || video != 20 {
		t.Fatalf("audio=%d video=%d", audio, video)
	}
}

func TestUnknownLengthSegmentEndsAtLegalFileBoundary(t *testing.T) {
	data := fixture.BuildMKV(fixture.MKVSpec{Seed: 18, Clusters: 1, UnknownSegment: true})
	doc, err := media.ParseEBML(data, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	segments := doc.FindDescendants(media.IDSegment)
	if len(segments) != 1 {
		t.Fatalf("segments=%d", len(segments))
	}
	seg := segments[0]
	if !seg.UnknownSize || seg.Size != -1 {
		t.Fatalf("segment unknown=%v size=%d", seg.UnknownSize, seg.Size)
	}
	if seg.End != int64(len(data)) {
		t.Fatalf("unknown segment end=%d file=%d", seg.End, len(data))
	}
	if _, err := media.ExtractBlocks(data, doc); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedCorruptionOffsetsAndNoForgedBlocks(t *testing.T) {
	samples := []fixture.CorruptSample{
		fixture.TruncatedVINTSample(),
		fixture.OutOfBoundsBlockSample(),
		fixture.IllegalBoundarySample(),
	}
	for _, sample := range samples {
		t.Run(sample.Name, func(t *testing.T) {
			doc, parseErr := media.ParseEBML(sample.Data, media.DefaultLimits())
			if parseErr == nil {
				t.Fatal("expected parse error")
			}
			if !media.IsInputCorruption(parseErr) {
				t.Fatalf("error not classified input corruption: %v", parseErr)
			}
			me, ok := media.AsError(parseErr)
			if !ok {
				t.Fatalf("error is not structured: %T", parseErr)
			}
			if me.Offset != sample.Defect.Offset {
				t.Fatalf("offset=%d embedded=%d delta=%d", me.Offset, sample.Defect.Offset, me.Offset-sample.Defect.Offset)
			}
			blocks, _ := media.ExtractBlocks(sample.Data, doc)
			for _, b := range blocks {
				if b.ElementOffset == sample.Defect.Offset {
					t.Fatalf("corrupt block was materialized at %d", b.ElementOffset)
				}
			}
		})
	}
}
