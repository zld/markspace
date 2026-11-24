package output

import (
	"bytes"
	"encoding/json"
	"testing"

	"mediasync/internal/model"
)

func sampleRecords() []Record {
	return []Record{
		{TrackID: 1, Kind: model.KindVideo, ExtSeq: 1, Keyframe: true, Original: 0, Corrected: 0, Size: 10},
		{TrackID: 2, Kind: model.KindAudio, ExtSeq: 5, Original: 1000, Corrected: 1000, Size: 20},
	}
}

func TestDigestStable(t *testing.T) {
	a := Digest(sampleRecords())
	b := Digest(sampleRecords())
	if a != b {
		t.Fatal("digest is not stable for identical input")
	}
	if len(a) != 64 {
		t.Errorf("digest length = %d, want 64 hex chars", len(a))
	}
}

func TestDigestChangesWithFields(t *testing.T) {
	base := sampleRecords()
	for name, mutate := range map[string]func([]Record){
		"corrected": func(r []Record) { r[0].Corrected = 1 },
		"original":  func(r []Record) { r[0].Original = 1 },
		"ext_seq":   func(r []Record) { r[0].ExtSeq = 2 },
		"keyframe":  func(r []Record) { r[0].Keyframe = false },
		"size":      func(r []Record) { r[0].Size = 11 },
		"track":     func(r []Record) { r[0].TrackID = 3 },
	} {
		recs := sampleRecords()
		mutate(recs)
		if Digest(recs) == Digest(base) {
			t.Errorf("%s change did not alter the digest", name)
		}
	}
}

func TestDigestOrderSensitive(t *testing.T) {
	r := sampleRecords()
	swapped := []Record{r[1], r[0]}
	if Digest(r) == Digest(swapped) {
		t.Error("delivery order must be part of the digest")
	}
}

func TestVerifyTruncation(t *testing.T) {
	cases := []struct {
		located, expected int64
		errors            int
		wantCode          int
		wantDev           int64
	}{
		{4096, 4096, 1, ExitCorrupted, 0},
		{4097, 4096, 1, ExitCorrupted, 1},
		{4095, 4096, 1, ExitCorrupted, 1},
		{4098, 4096, 1, ExitFailure, 2},
		{-1, 4096, 1, ExitFailure, 4097},
		{4096, 4096, 0, ExitFailure, 0},
	}
	for _, c := range cases {
		code, dev := VerifyTruncation(c.located, c.expected, c.errors)
		if code != c.wantCode || dev != c.wantDev {
			t.Errorf("VerifyTruncation(%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.located, c.expected, c.errors, code, dev, c.wantCode, c.wantDev)
		}
	}
}

func TestCheckpointVerify(t *testing.T) {
	if ok, msg := CheckpointVerify("abc", "abc"); !ok || msg != "" {
		t.Errorf("matching digests rejected: %v %q", ok, msg)
	}
	if ok, msg := CheckpointVerify("abc", "def"); ok || msg == "" {
		t.Error("mismatched digests must fail with a message")
	}
	if ok, _ := CheckpointVerify("", "abc"); ok {
		t.Error("empty digest must fail")
	}
}

func TestWriteJSONFields(t *testing.T) {
	var buf bytes.Buffer
	err := WriteJSON(&buf, Summary{
		Command: "demo", PacketTotal: 10, Lost: 2, Recovered: 1,
		LateRecomputed: 3, MaxAVSkewMS: 1.5, Digest: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"packet_total", "lost", "recovered", "late_recomputed",
		"max_av_skew_ms", "sha256", "resume_elapsed_ms",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("summary missing %q", key)
		}
	}
}
