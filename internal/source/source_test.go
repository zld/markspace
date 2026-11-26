package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediarepair/internal/media"
	"mediarepair/internal/source"
	"mediarepair/internal/testfixture"
)

func TestStandardManifestAndSampleTable(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.Manifest.Frames) != 12 {
		t.Fatalf("frames=%d want 12", len(fx.Manifest.Frames))
	}
	if err := source.ValidateFrames(root, fx.Manifest); err != nil {
		t.Fatal(err)
	}
	mp4Path, err := filepath.Abs(filepath.Join(root, fx.Manifest.MP4.Path))
	if err != nil {
		t.Fatal(err)
	}
	st, err := source.ReadSampleTable(mp4Path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.SampleOffsets) != 12 {
		t.Fatalf("samples=%d want 12", len(st.SampleOffsets))
	}
	for _, s := range st.SampleOffsets {
		if s.Offset+int64(s.Size) > st.FileSize {
			t.Fatalf("sample %d out of bounds", s.Index)
		}
	}
}

func TestManifestRejectionsReportFrameAndCode(t *testing.T) {
	cases := []struct {
		name   string
		change func(*source.Manifest)
		code   media.Code
	}{
		{"duplicate", func(m *source.Manifest) { m.Frames[1].Sequence = m.Frames[0].Sequence }, media.CodeDuplicateSeq},
		{"checksum", func(m *source.Manifest) {
			m.Frames[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}, media.CodeChecksum},
		{"format", func(m *source.Manifest) { m.Frames[0].Path = "frames/frame.gif"; m.Frames[0].Format = "gif" }, media.CodeFormat},
		{"dimensions", func(m *source.Manifest) { m.Frames[0].Width = 999999 }, media.CodeDimensions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fx, err := testfixture.CreateStandard(root)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(fx.Manifest)
			err = source.ValidateFrames(root, fx.Manifest)
			if !media.HasCode(err, tc.code) {
				t.Fatalf("err=%v want code %s", err, tc.code)
			}
			me := media.AsError(err)
			if me == nil || me.Frame == 0 || me.File == "" || me.Stage == "" {
				t.Fatalf("error location incomplete: %#v", me)
			}
		})
	}
}

func TestTruncatedMP4AndBadOffsets(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(root, "truncated.mp4")
	if err := os.WriteFile(truncated, fx.MP4Bytes[:len(fx.MP4Bytes)-48], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = source.ReadSampleTable(truncated, 1)
	if !media.HasCode(err, media.CodeMP4Truncated) {
		t.Fatalf("truncated err=%v", err)
	}
	if me := media.AsError(err); me == nil || me.Offset < 0 {
		t.Fatalf("expected available offset in %#v", me)
	}

	mp4Path := filepath.Join(root, fx.Manifest.MP4.Path)
	st, err := source.ReadSampleTable(mp4Path, 1)
	if err != nil {
		t.Fatal(err)
	}
	st.Path = mp4Path
	st.SampleOffsets[3].Offset = st.FileSize + 100
	err = source.ValidateSampleOffsets(st)
	if !media.HasCode(err, media.CodeOffsetOutOfRange) {
		t.Fatalf("offset err=%v", err)
	}
	if me := media.AsError(err); me == nil || me.Offset != st.SampleOffsets[3].Offset {
		t.Fatalf("bad offset error=%#v", me)
	}
}

func TestPathEscapeRejectedBeforeOpen(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	fx.Manifest.Frames[0].Path = "../../escape.png"
	err = source.ValidateFrames(root, fx.Manifest)
	if !media.HasCode(err, media.CodePathEscape) {
		t.Fatalf("escape err=%v", err)
	}
}
