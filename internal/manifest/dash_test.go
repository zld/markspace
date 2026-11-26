package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/clock"
)

// writeTree creates name->content pairs under a fresh temp directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const mpdOK = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT8S">
  <Period>
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="1000" media="$RepresentationID$/seg-$Number$.m4s"
        initialization="$RepresentationID$/init.mp4" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="2000" r="1"/>
          <S t="4000" d="2000" r="1"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v400" bandwidth="400000"/>
      <Representation id="v800" bandwidth="800000"/>
    </AdaptationSet>
  </Period>
</MPD>`

func mpdFiles(mpd string) map[string]string {
	files := map[string]string{"prog.mpd": mpd}
	for _, rep := range []string{"v400", "v800"} {
		for n := 1; n <= 4; n++ {
			files[rep+"/seg-"+itoa(n)+".m4s"] = "media"
		}
		files[rep+"/init.mp4"] = "init"
	}
	return files
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestParseDASHOK(t *testing.T) {
	dir := writeTree(t, mpdFiles(mpdOK))
	m, err := ParseDASH(filepath.Join(dir, "prog.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Representations) != 2 {
		t.Fatalf("representations = %d", len(m.Representations))
	}
	rep := m.Representations[0]
	if rep.ID != "v400" || rep.BandwidthBPS != 400000 {
		t.Fatalf("repr = %+v", rep)
	}
	if len(rep.Segments) != 4 {
		t.Fatalf("segments = %d", len(rep.Segments))
	}
	// Timeline: 0,2,4,6 seconds; each 2s long.
	wantStarts := []string{"0", "2", "4", "6"}
	for i, s := range rep.Segments {
		if c, _ := s.Start.Cmp(clock.MustR(parseInt(t, wantStarts[i]), 1)); c != 0 {
			t.Errorf("seg %d start = %s", i, s.Start)
		}
		if c, _ := s.Duration.Cmp(clock.MustR(2, 1)); c != 0 {
			t.Errorf("seg %d duration = %s", i, s.Duration)
		}
		if s.Line == 0 {
			t.Errorf("seg %d missing source line", i)
		}
		if s.ByteSize != 5 {
			t.Errorf("seg %d size = %d", i, s.ByteSize)
		}
	}
	if c, _ := m.MediaPresentSec.Cmp(clock.MustR(8, 1)); c != 0 {
		t.Errorf("mediaPresent = %s", m.MediaPresentSec)
	}
}

func parseInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestParseDASHDuplicateReference(t *testing.T) {
	// $Time$ template plus two timeline entries at the same t=0: both
	// expand to the identical URL, which is a duplicate reference.
	bad := `<?xml version="1.0"?>
<MPD mediaPresentationDuration="PT4S"><Period><AdaptationSet>
  <SegmentTemplate timescale="1000" media="v/seg-$Time$.m4s" startNumber="1">
    <SegmentTimeline>
      <S t="0" d="2000"/>
      <S t="0" d="2000"/>
    </SegmentTimeline>
  </SegmentTemplate>
  <Representation id="v" bandwidth="100000"/>
</AdaptationSet></Period></MPD>`
	files := map[string]string{"prog.mpd": bad, "v/seg-0.m4s": "x"}
	dir := writeTree(t, files)
	_, err := ParseDASH(filepath.Join(dir, "prog.mpd"))
	me := clock.AsMedia(err)
	if me == nil || me.Kind != clock.KindInvalidInput || me.Line == 0 {
		t.Fatalf("want structured line-located error, got %+v", err)
	}
}

func TestParseDASHMissingSegment(t *testing.T) {
	dir := writeTree(t, mpdFiles(mpdOK))
	if err := os.Remove(filepath.Join(dir, "v400", "seg-3.m4s")); err != nil {
		t.Fatal(err)
	}
	_, err := ParseDASH(filepath.Join(dir, "prog.mpd"))
	me := clock.AsMedia(err)
	if me == nil || me.Kind != clock.KindInvalidInput {
		t.Fatalf("want invalid input for missing segment, got %v", err)
	}
}

func TestSafePathRejects(t *testing.T) {
	dir := t.TempDir()
	bad := []string{"https://evil.test/a.m4s", "//host/a", "/etc/passwd", "../escape.m4s",
		"a/../../b.m4s"}
	for _, ref := range bad {
		if _, err := SafePath(dir, ref); err == nil {
			t.Errorf("expected rejection of %q", ref)
		}
	}
	good, err := SafePath(dir, "a/b/../c.m4s")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(good) != filepath.Join(dir, "a") {
		t.Fatalf("unexpected clean path %q", good)
	}
}

func TestISODuration(t *testing.T) {
	cases := map[string]string{
		"PT8S":     "8",
		"PT1.5S":   "3/2",
		"PT1H30M":  "5400",
		"P1DT12H":  "129600",
		"PT0.250S": "1/4",
	}
	for in, want := range cases {
		got, err := parseISODuration(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if c, _ := got.Cmp(clock.MustR(parseRat(want))); c != 0 {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
	if _, err := parseISODuration("8S"); err == nil {
		t.Fatal("expected error without P prefix")
	}
}

func parseRat(s string) (int64, int64) {
	for i, c := range s {
		if c == '/' {
			return parseInt2(s[:i]), parseInt2(s[i+1:])
		}
	}
	return parseInt2(s), 1
}

func parseInt2(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}
