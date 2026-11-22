package ingest

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/core"
	"mediad/internal/diagnose"
)

func rtpPacket(seq uint16, ts uint32) []byte {
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 0x01020304},
		Payload: []byte{0x00, 0x00, 0x01},
	}
	b, _ := p.Marshal()
	return b
}

func appendLengthPrefixed(dst, pkt []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(pkt)))
	dst = append(dst, n[:]...)
	return append(dst, pkt...)
}

func hasCategory(is diagnose.Issues, c diagnose.Category) bool {
	for _, i := range is {
		if i.Category == c {
			return true
		}
	}
	return false
}

func asset(name string, data string) *Asset {
	return &Asset{Name: name, Data: []byte(data), Format: FormatOf(name)}
}

func TestParseSRTBasic(t *testing.T) {
	src := "1\n00:00:01,500 --> 00:00:03,000\nhello\nworld\n\n2\n00:00:04,000 --> 00:00:05,250\nbye\n"
	subs, err := ParseSRT(asset("a.srt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 2 {
		t.Fatalf("cues=%d want 2", len(subs.Cues))
	}
	if got := subs.Cues[0].Top.String(); got != "3/2" {
		t.Fatalf("top=%s", got)
	}
	if got := subs.Cues[0].Bottom.String(); got != "3/1" {
		t.Fatalf("bottom=%s", got)
	}
	if len(subs.Cues[0].Lines) != 2 || subs.Cues[0].Lines[1] != "world" {
		t.Fatalf("lines=%v", subs.Cues[0].Lines)
	}
	if len(subs.Issues) != 0 {
		t.Fatalf("unexpected issues: %v", subs.Issues)
	}
}

func TestParseSRTBOMAndMicroseconds(t *testing.T) {
	src := "\ufeff1\r\n00:00:00,123456 --> 00:00:01,500000\r\nhi\r\n"
	subs, err := ParseSRT(asset("b.srt", src))
	if err != nil {
		t.Fatal(err)
	}
	if subs.BOM != "utf-8" {
		t.Fatalf("bom=%q", subs.BOM)
	}
	if len(subs.Cues) != 1 {
		t.Fatalf("cues=%d", len(subs.Cues))
	}
	c := subs.Cues[0]
	if c.Precision != "microseconds" {
		t.Fatalf("precision=%s", c.Precision)
	}
	if got := c.Top.String(); got != "1929/15625" {
		t.Fatalf("top=%s", got)
	}
	// The timing span must point after the BOM.
	if c.TimingSpan.Start < 3 {
		t.Fatalf("timing span starts at %d", c.TimingSpan.Start)
	}
}

func TestParseSRTBadCueContinues(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\n00:00:99,000 --> 00:00:03,000\nbad\n\n3\n00:00:05,000 --> 00:00:06,000\nlast\n"
	subs, err := ParseSRT(asset("c.srt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 2 {
		t.Fatalf("cues=%d want 2", len(subs.Cues))
	}
	if len(subs.Issues) != 1 || subs.Issues[0].Category != diagnose.CatSRTTime {
		t.Fatalf("issues=%v", subs.Issues)
	}
	line := strings.Index(src, "00:00:99")
	if got := subs.Issues[0].Span.Start; got != int64(line) {
		t.Fatalf("offset=%d want %d", got, line)
	}
	if subs.Cues[1].Lines[0] != "last" {
		t.Fatalf("lost following cue: %v", subs.Cues[1])
	}
}

func TestParseSRTTruncatedBody(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,000\nunterminated"
	subs, err := ParseSRT(asset("d.srt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 1 {
		t.Fatalf("cues=%d", len(subs.Cues))
	}
	if len(subs.Issues) != 1 || subs.Issues[0].Category != diagnose.CatTruncatedCue {
		t.Fatalf("issues=%v", subs.Issues)
	}
	if got := subs.Issues[0].Span.End; got != int64(len(src)) {
		t.Fatalf("span end=%d want %d", got, len(src))
	}
}

func TestParseVTTBasicAndSettings(t *testing.T) {
	src := "WEBVTT - demo\n\nNOTE ignore me\n\ncue-1\n00:01.000 --> 00:02.500 align:start position:10%\nfirst\nsecond\n\n01:00:00.000 --> 01:00:01.000\nlater\n"
	subs, err := ParseVTT(asset("e.vtt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 2 {
		t.Fatalf("cues=%d want 2: %v", len(subs.Cues), subs.Issues)
	}
	c := subs.Cues[0]
	if c.ID != "cue-1" {
		t.Fatalf("id=%q", c.ID)
	}
	if len(c.Settings) != 2 || c.Settings[0] != "align:start" {
		t.Fatalf("settings=%v", c.Settings)
	}
	if got := c.Top.String(); got != "1/1" {
		t.Fatalf("top=%s", got)
	}
	if got := subs.Cues[1].Top.String(); got != "3600/1" {
		t.Fatalf("later top=%s", got)
	}
}

func TestParseVTTInvalidTimingDoesNotPoisonNextCue(t *testing.T) {
	src := "WEBVTT\n\n00:01.000 --> 00:02.000\ngood\n\n00:03.000 --> 00:0x.000\nbad\n\n00:04.000 --> 00:05.000\nafter\n"
	subs, err := ParseVTT(asset("f.vtt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 2 {
		t.Fatalf("cues=%d want 2", len(subs.Cues))
	}
	if len(subs.Issues) != 1 || subs.Issues[0].Category != diagnose.CatVTTTime {
		t.Fatalf("issues=%v", subs.Issues)
	}
	if subs.Cues[1].Lines[0] != "after" {
		t.Fatalf("following cue polluted: %v", subs.Cues[1])
	}
}

func TestParseVTTMissingHeader(t *testing.T) {
	subs, err := ParseVTT(asset("g.vtt", "00:01.000 --> 00:02.000\nx\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 1 {
		t.Fatalf("cues=%d", len(subs.Cues))
	}
	if len(subs.Issues) != 1 || subs.Issues[0].Category != diagnose.CatVTTTime {
		t.Fatalf("issues=%v", subs.Issues)
	}
}

func TestParseTimecodeRates(t *testing.T) {
	cases := []struct {
		raw  string
		rate FrameRate
		want string
		frm  int64
		err  bool
	}{
		{"01:00:00:00", Rate24, "3600/1", 86400, false},
		{"00:00:00:01", Rate25, "1/25", 1, false},
		{"00:00:00;00", Rate2997, "0/1", 0, false},
		{"00:01:00;02", Rate2997, "3003/50", 1800, false},
		{"00:01:00;00", Rate2997, "", 0, true},                  // frames 00 dropped
		{"00:01:00;01", Rate2997, "", 0, true},                  // frame 01 dropped
		{"00:10:00;00", Rate2997, "2999997/5000", 17982, false}, // tenth minute keeps 00
		{"00:00:00;01", Rate25, "", 0, true},                    // drop not allowed at 25
		{"00:00:00:25", Rate25, "", 0, true},                    // frame 25 does not exist
		{"1:2:3:4", Rate24, "22339/6", 89356, false},            // short fields are accepted
	}
	for _, tc := range cases {
		got, err := ParseTimecode(tc.raw, tc.rate, span(0, 11))
		if tc.err {
			if err == nil {
				t.Errorf("%s: want error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.raw, err)
			continue
		}
		if got.Top.String() != tc.want || got.FramesTotal != tc.frm {
			t.Errorf("%s: got %s frames=%d want %s frames=%d", tc.raw, got.Top, got.FramesTotal, tc.want, tc.frm)
		}
	}
}

func TestTimecodeRoundTrip(t *testing.T) {
	for _, rate := range Rates() {
		for _, drop := range []bool{false, true} {
			if drop && !rate.Drop {
				continue
			}
			for f := int64(0); f < 30000; f += 997 {
				label := FramesToTimecode(f, rate, drop)
				tc, err := ParseTimecode(label, rate, span(0, 11))
				if err != nil {
					t.Fatalf("rate=%s drop=%v frames=%d label=%s: %v", rate.Name, drop, f, label, err)
				}
				if tc.FramesTotal != f {
					t.Fatalf("rate=%s drop=%v frames=%d label=%s parsed=%d", rate.Name, drop, f, label, tc.FramesTotal)
				}
			}
		}
	}
}

func TestParseRTPWrapAndGap(t *testing.T) {
	var buf []byte
	seq := uint16(65530)
	ts := uint32(0xFFFFFFF0)
	for i := 0; i < 6; i++ {
		pkt := rtpPacket(seq, ts)
		buf = appendLengthPrefixed(buf, pkt)
		seq++
		if i == 2 {
			seq += 2 // inject a gap
		}
		ts += 1000
	}
	st, err := ParseRTP(asset("s.rtp", string(buf)), 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 6 {
		t.Fatalf("records=%d", len(st.Records))
	}
	if st.Wraps != 1 {
		t.Fatalf("wraps=%d", st.Wraps)
	}
	if !st.RTPTimeContinuity() {
		t.Fatal("timestamps regressed after wrap")
	}
	if st.GapLoss != 2 {
		t.Fatalf("gap loss=%d", st.GapLoss)
	}
	if st.Records[0].Timestamp == 0 || st.Records[0].ExtTS != int64(0xFFFFFFF0) {
		t.Fatalf("raw/ext must be preserved: %+v", st.Records[0])
	}
	if st.Records[3].ExtTS <= st.Records[2].ExtTS {
		t.Fatalf("extended clock not monotonic: %+v", st.Records)
	}
	if !hasCategory(st.IssueList, diagnose.CatRTPWrap) || !hasCategory(st.IssueList, diagnose.CatRTPSequence) {
		t.Fatalf("issues=%v", st.IssueList)
	}
}

func TestParseRTPTruncatedPrefix(t *testing.T) {
	good := appendLengthPrefixed(nil, rtpPacket(1, 100))
	bad := append(append([]byte{}, good...), 0, 0, 0, 0xff, 0x00)
	st, err := ParseRTP(asset("t.rtp", string(bad)), 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 1 {
		t.Fatalf("records=%d", len(st.Records))
	}
	if len(st.IssueList) != 1 || st.IssueList[0].Span.Start != int64(len(good)) {
		t.Fatalf("issues=%v", st.IssueList)
	}
}

func TestLoadRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir, "../x.srt", DefaultLimits()); err == nil {
		t.Fatal("want traversal rejection")
	}
	if _, err := Load(dir, "/etc/passwd", DefaultLimits()); err == nil {
		t.Fatal("want absolute path rejection")
	}
}

func TestReadAssetCap(t *testing.T) {
	lim := Limits{MaxAssetBytes: 8, MaxAssets: 4}
	if _, err := ReadAsset(strings.NewReader(strings.Repeat("x", 9)), "a.srt", lim); err == nil {
		t.Fatal("want cap error")
	}
}

func span(start, end int64) core.Span { return core.Span{Start: start, End: end} }

func TestTimecodeShortFieldsAndOptions(t *testing.T) {
	// "MM:SS:FF" is accepted only when explicitly requested.
	if _, err := ParseTimecode("01:02:03", Rate25, span(0, 8)); err == nil {
		t.Fatal("short timecode accepted without the option")
	}
	tc, err := ParseTimecode("01:02:03", Rate25, span(0, 8), AllowMissingHours())
	if err != nil {
		t.Fatal(err)
	}
	if tc.Hours != 0 || tc.Minutes != 1 || tc.Seconds != 2 || tc.Frames != 3 {
		t.Fatalf("parsed=%+v", tc)
	}
	want := int64((62*25 + 3))
	if tc.FramesTotal != want {
		t.Fatalf("frames=%d want %d", tc.FramesTotal, want)
	}
}

func TestTimecodeRejectsMalformedSeparators(t *testing.T) {
	bad := []string{"00:00:00.00", "00;00;00;00", "00:00:00:00;", "00:00:00;00;00", "aa:00:00:00", "00:60:00:00", "00:00:60:00", "00:00:00:999"}
	for _, raw := range bad {
		if _, err := ParseTimecode(raw, Rate2997, span(0, int64(len(raw)))); err == nil {
			t.Errorf("%q: want error", raw)
		}
	}
}

func TestRTPOutOfOrderIsDistinguishedFromLoss(t *testing.T) {
	var buf []byte
	for _, seq := range []uint16{10, 11, 14, 12, 13} {
		buf = appendLengthPrefixed(buf, rtpPacket(seq, uint32(seq)*3000))
	}
	st, err := ParseRTP(asset("o.rtp", string(buf)), 90000)
	if err != nil {
		t.Fatal(err)
	}
	if st.GapLoss != 2 {
		t.Fatalf("gap loss=%d", st.GapLoss)
	}
	// 14 -> 12 is one late packet; 12 -> 13 then advances normally.
	if st.OutOfOrder != 1 {
		t.Fatalf("out of order=%d", st.OutOfOrder)
	}
	for _, is := range st.IssueList {
		if is.Category == diagnose.CatRTPSequence && is.Span.Start == 0 {
			t.Fatalf("finding without an offset: %+v", is)
		}
	}
}

func TestParseSRTSecondPrecisionAndDotFraction(t *testing.T) {
	// Some encoders emit "00:00:01.500"; the seconds fraction accepts '.'
	// when it is not the field separator position of a WebVTT timestamp.
	src := "1\n00:00:01.500 --> 00:00:02.000\nhi\n"
	subs, err := ParseSRT(asset("dot.srt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 1 {
		t.Fatalf("cues=%d issues=%v", len(subs.Cues), subs.Issues)
	}
	if subs.Cues[0].Top.String() != "3/2" {
		t.Fatalf("top=%s", subs.Cues[0].Top)
	}
}

func TestParseVTTRejectsCommaFractions(t *testing.T) {
	src := "WEBVTT\n\n00:01,000 --> 00:02,000\nwrong\n\n00:03.000 --> 00:04.000\nright\n"
	subs, err := ParseVTT(asset("comma.vtt", src))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 1 || len(subs.Issues) != 1 {
		t.Fatalf("cues=%d issues=%v", len(subs.Cues), subs.Issues)
	}
	if subs.Cues[0].Lines[0] != "right" {
		t.Fatalf("following cue polluted: %v", subs.Cues[0])
	}
	if subs.Issues[0].Span.Start == 0 {
		t.Fatalf("issue has no offset: %+v", subs.Issues[0])
	}
}

func TestUTF16BOMIsReportedNotParsed(t *testing.T) {
	a := &Asset{Name: "u.srt", Format: "srt", Data: []byte{0xff, 0xfe, 'h', 0, 'i', 0}}
	subs, err := ParseSRT(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Cues) != 0 {
		t.Fatalf("UTF-16 input produced %d cues", len(subs.Cues))
	}
	if len(subs.Issues) != 1 || subs.Issues[0].Category != diagnose.CatUnsupported {
		t.Fatalf("issues=%v", subs.Issues)
	}
	if subs.Issues[0].Span != (core.Span{Start: 0, End: 2}) {
		t.Fatalf("span=%+v", subs.Issues[0].Span)
	}
}
