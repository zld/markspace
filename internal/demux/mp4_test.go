package demux

import (
	"bytes"
	"crypto/sha256"
	"testing"

	merr "mediacheck/internal/errors"
	"mediacheck/internal/testmedia"
)

func parseBytes(t *testing.T, b []byte) *Container {
	t.Helper()
	c, err := Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return c
}

func TestParseHealthyContainer(t *testing.T) {
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	c := parseBytes(t, media.Container)
	if c.MovieTimescale != 90000 {
		t.Fatalf("movie timescale = %d; want 90000", c.MovieTimescale)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks = %d; want 2", len(c.Tracks))
	}
	video, audio := c.Tracks[0], c.Tracks[1]
	if video.Kind != KindVideo || video.Codec != "avc1" || video.Timescale != 90000 {
		t.Fatalf("video track wrong: %+v", video)
	}
	if audio.Kind != KindAudio || audio.Codec != "mp4a" || audio.Timescale != 48000 {
		t.Fatalf("audio track wrong: %+v", audio)
	}
	if len(video.Samples) != 48 || len(audio.Samples) != 96 {
		t.Fatalf("sample counts = %d/%d", len(video.Samples), len(audio.Samples))
	}
	// Keyframes: every 12th video sample is sync; first sample always sync.
	if !video.Samples[0].Sync || video.Samples[1].Sync || !video.Samples[12].Sync {
		t.Fatal("keyframe positions wrong")
	}
	if !audio.Samples[0].Sync {
		t.Fatal("audio samples must be sync-capable")
	}
	// Timing integrity: constant delta, monotonic DTS.
	for i, s := range video.Samples {
		if s.DTS != int64(i)*3600 || s.Duration != 3600 {
			t.Fatalf("video sample %d timing = %+v", i, s)
		}
	}
	if video.EndTick() != 48*3600 || audio.EndTick() != 96*1024 {
		t.Fatalf("end ticks wrong: v=%d a=%d", video.EndTick(), audio.EndTick())
	}
}

func TestSampleBytesPreserved(t *testing.T) {
	media, _ := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	c := parseBytes(t, media.Container)
	r := bytes.NewReader(media.Container)
	for ti, tr := range c.Tracks {
		for si, s := range tr.Samples {
			got, err := c.ReadSample(r, tr, si)
			if err != nil {
				t.Fatalf("read sample t%d s%d: %v", ti, si, err)
			}
			want := media.Tracks[ti].Samples[si]
			if sha256.Sum256(got) != sha256.Sum256(want) {
				t.Fatalf("sample bytes changed t%d s%d", ti, si)
			}
			if len(got) != s.Size {
				t.Fatalf("sample size mismatch t%d s%d", ti, si)
			}
		}
	}
}

func TestTruncatedBoxReportsCodeAndOffset(t *testing.T) {
	media, _ := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	mdatOff := testmedia.FindTopLevelBox(media.Container, "mdat")

	cases := map[string][]byte{
		"truncated payload": testmedia.TruncateAt(media.Container, mdatOff+12),
		"inflated size":     testmedia.InflateBoxSize(media.Container, mdatOff),
		"header cut":        testmedia.TruncateAt(media.Container, mdatOff+4),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(bytes.NewReader(bad))
			e, ok := merr.As(err)
			if !ok || e.Code != merr.CodeTruncatedBox {
				t.Fatalf("got %v; want stable truncation code", err)
			}
			if e.Offset != int64(mdatOff) {
				t.Fatalf("offset = %d; want %d", e.Offset, mdatOff)
			}
			if e.State != merr.StateFatal {
				t.Fatal("truncation must be fatal")
			}
		})
	}
}

func TestExtremeTimescaleAccepted(t *testing.T) {
	spec := testmedia.DefaultSpec(7)
	// Timescale close to the uint32 ceiling must parse without overflow in
	// the duration/offset arithmetic used by the table builder.
	spec.Tracks[0].Timescale = 0x7fffffff
	spec.Tracks[0].Delta = 0x7fffffff / 25
	media, err := testmedia.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	c := parseBytes(t, media.Container)
	tr := c.Tracks[0]
	if tr.Timescale != 0x7fffffff {
		t.Fatalf("timescale = %d", tr.Timescale)
	}
	last := tr.Samples[len(tr.Samples)-1]
	if last.DTS <= 0 || last.DTS+last.Duration <= last.DTS {
		t.Fatal("extreme timescale timing overflowed")
	}
}

func TestUnsupportedSampleEntryStillReported(t *testing.T) {
	// An unknown fourCC must survive parsing with its codec tag intact; the
	// mapping layer owns the compatibility decision.
	spec := testmedia.DefaultSpec(7)
	spec.Tracks[0].Codec = "zyz1"
	media, err := testmedia.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	c := parseBytes(t, media.Container)
	if c.Tracks[0].Codec != "zyz1" {
		t.Fatalf("codec = %q; want zyz1", c.Tracks[0].Codec)
	}
}
