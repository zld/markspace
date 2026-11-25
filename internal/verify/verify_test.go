package verify

import (
	"encoding/binary"
	"testing"

	"rtpregress/internal/media"
)

func sampleArtifact(t *testing.T) (*Artifact, []byte, Expected) {
	t.Helper()
	events := []WriteEvent{
		{TimeNs: 0, Segment: 0, Track: "subtitle", Source: "subtitle", Text: "cue one"},
		{TimeNs: 500_000_000, Segment: 0, Track: "chapter", Source: "chapter", Text: "ch1"},
		{TimeNs: 900_000_000, Segment: 1, Track: "meta", Source: "emsg", Text: "emsg one"},
		{TimeNs: 1_800_000_000, Segment: 1, Track: "id3", Source: "id3", Text: "id3 cue"},
	}
	segs := []Segment{
		{StartNs: 0, EventCount: 2},
		{StartNs: 900_000_000, EventCount: 2},
	}
	data, err := Encode(events, segs, WriteParams{SegmentMS: 1000, PrecisionUS: 20})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Decode(data)
	if err != nil {
		t.Fatalf("decode fresh artifact: %v", err)
	}
	exp := Expected{
		Events: 4, MaxDeviationNs: 10_000, MaxBytes: 4096, QualityMin: 0.95,
		Quality: 0.97, SegmentMS: 1000, PrecisionUS: 20, Segments: 2,
	}
	return a, data, exp
}

func TestRoundTripAndCheck(t *testing.T) {
	a, _, exp := sampleArtifact(t)
	res, err := Check(a, exp)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("fresh artifact failed: %+v", res.Mismatch)
	}
	if res.Summary.Events != 4 {
		t.Fatalf("events = %d", res.Summary.Events)
	}
	if res.Summary.BySource["emsg"] != 1 || res.Summary.ByTrack["chapter"] != 1 {
		t.Fatalf("summary maps = %+v", res.Summary)
	}
	if res.Summary.FileSHA256 == "" || res.Summary.BodySHA256 == "" {
		t.Fatal("digests missing")
	}
	if a.Records[0].Text != "cue one" || a.Records[3].Source != "id3" {
		t.Fatalf("records = %+v", a.Records)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	_, data1, _ := sampleArtifact(t)
	events := []WriteEvent{
		{TimeNs: 0, Segment: 0, Track: "subtitle", Source: "subtitle", Text: "cue one"},
		{TimeNs: 500_000_000, Segment: 0, Track: "chapter", Source: "chapter", Text: "ch1"},
		{TimeNs: 900_000_000, Segment: 1, Track: "meta", Source: "emsg", Text: "emsg one"},
		{TimeNs: 1_800_000_000, Segment: 1, Track: "id3", Source: "id3", Text: "id3 cue"},
	}
	segs := []Segment{{StartNs: 0, EventCount: 2}, {StartNs: 900_000_000, EventCount: 2}}
	data2, err := Encode(events, segs, WriteParams{SegmentMS: 1000, PrecisionUS: 20})
	if err != nil {
		t.Fatal(err)
	}
	if DigestBytesForTest(data1) != DigestBytesForTest(data2) {
		t.Fatal("encode is not deterministic")
	}
}

func DigestBytesForTest(b []byte) string { return SHA256(b) }

func TestDecodeRejectsCorruptStructures(t *testing.T) {
	_, data, _ := sampleArtifact(t)

	short := append([]byte(nil), data[:HeaderSize-1]...)
	if _, err := Decode(short); err == nil {
		t.Fatal("short file not rejected")
	}

	badMagic := append([]byte(nil), data...)
	badMagic[0] = 'X'
	if _, err := Decode(badMagic); err == nil {
		t.Fatal("bad magic not rejected")
	}

	badVersion := append([]byte(nil), data...)
	binary.BigEndian.PutUint16(badVersion[4:6], 9)
	if _, err := Decode(badVersion); err == nil {
		t.Fatal("bad version not rejected")
	}

	// Flip one text byte: the embedded body digest must catch it.
	tampered := append([]byte(nil), data...)
	last := len(tampered) - 1
	tampered[last] ^= 0x1
	_, err := Decode(tampered)
	if err == nil {
		t.Fatal("body tamper not detected")
	}
	if media.KindOf(err) != media.KindVerify {
		t.Fatalf("kind = %s", media.KindOf(err))
	}

	// Declare more events than the body holds.
	overCount := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(overCount[16:20], 999)
	if _, err := Decode(overCount); err == nil {
		t.Fatal("over-declared event count not rejected")
	}
}

func TestCheckLocatesFirstMismatch(t *testing.T) {
	a, _, exp := sampleArtifact(t)

	// Wrong event count is reported at the first record.
	exp.Events = 5
	res, _ := Check(a, exp)
	if res.OK {
		t.Fatal("count mismatch accepted")
	}
	if res.Mismatch.Check != "event-count" {
		t.Fatalf("check = %s", res.Mismatch.Check)
	}

	// A deviation over the limit names the offending record's track/offset.
	a2, _, exp2 := sampleArtifact(t)
	// Corrupt record 2's timestamp in a way the digest cannot see: rebuild
	// an artifact whose event actually sits off-grid.
	events := []WriteEvent{
		{TimeNs: 0, Segment: 0, Track: "subtitle", Source: "subtitle", Text: "cue one"},
		{TimeNs: 500_007_000, Segment: 0, Track: "chapter", Source: "chapter", Text: "ch1"},
		{TimeNs: 900_000_000, Segment: 1, Track: "meta", Source: "emsg", Text: "emsg one"},
		{TimeNs: 1_800_000_000, Segment: 1, Track: "id3", Source: "id3", Text: "id3 cue"},
	}
	segs := []Segment{{StartNs: 0, EventCount: 2}, {StartNs: 900_000_000, EventCount: 2}}
	data, err := Encode(events, segs, WriteParams{SegmentMS: 1000, PrecisionUS: 20})
	if err != nil {
		t.Fatal(err)
	}
	a2, err = Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	exp2.MaxDeviationNs = 10_000 // 7_000ns off-grid? 500007000 % 20000 = 7000, within 10us
	res2, _ := Check(a2, exp2)
	if !res2.OK {
		t.Fatalf("7us off-grid should pass 10us limit: %+v", res2.Mismatch)
	}
	exp2.MaxDeviationNs = 5_000
	res3, _ := Check(a2, exp2)
	if res3.OK {
		t.Fatal("deviation over limit accepted")
	}
	if res3.Mismatch.Track != "chapter" || !res3.Mismatch.HasOffset {
		t.Fatalf("mismatch not localized: %+v", res3.Mismatch)
	}

	// Byte cap.
	a3, _, exp3 := sampleArtifact(t)
	exp3.MaxBytes = 16
	res4, _ := Check(a3, exp3)
	if res4.OK || res4.Mismatch.Check != "max-bytes" {
		t.Fatalf("byte cap not enforced: %+v", res4.Mismatch)
	}

	// Quality floor.
	a4, _, exp4 := sampleArtifact(t)
	exp4.QualityMin = 0.99
	res5, _ := Check(a4, exp4)
	if res5.OK || res5.Mismatch.Check != "quality-floor" {
		t.Fatalf("quality floor not enforced: %+v", res5.Mismatch)
	}
}

func TestCheckSegmentCountMismatch(t *testing.T) {
	a, _, exp := sampleArtifact(t)
	exp.Segments = 3
	res, _ := Check(a, exp)
	if res.OK || res.Mismatch.Check != "segment-count" {
		t.Fatalf("segment count mismatch: %+v", res.Mismatch)
	}
}

func TestCheckParameterEcho(t *testing.T) {
	a, _, exp := sampleArtifact(t)
	exp.SegmentMS = 500
	res, _ := Check(a, exp)
	if res.OK || res.Mismatch.Check != "parameter-echo" {
		t.Fatalf("parameter echo: %+v", res.Mismatch)
	}
}

func TestEncodeRejectsBadIdentifiers(t *testing.T) {
	segs := []Segment{{StartNs: 0, EventCount: 1}}
	if _, err := Encode([]WriteEvent{{Track: "unknown", Source: "subtitle", Segment: 0}}, segs, WriteParams{SegmentMS: 1000, PrecisionUS: 20}); err == nil {
		t.Fatal("unknown track accepted")
	}
	if _, err := Encode([]WriteEvent{{Track: "meta", Source: "unknown", Segment: 0}}, segs, WriteParams{SegmentMS: 1000, PrecisionUS: 20}); err == nil {
		t.Fatal("unknown source accepted")
	}
	if _, err := Encode(nil, segs, WriteParams{SegmentMS: 0, PrecisionUS: 20}); err == nil {
		t.Fatal("invalid params accepted")
	}
}

func TestStableJSONSortsKeys(t *testing.T) {
	a, err := StableJSON(map[string]any{"z": 1, "a": 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"a":2,"z":1}` {
		t.Fatalf("stable json = %s", a)
	}
}
