package manifest

import (
	"testing"

	"mediacore/internal/clock"
)

func TestAlignmentError(t *testing.T) {
	a, err := Decode([]byte(legacyJSON))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Decode([]byte(legacyJSON))
	worst, err := AlignmentError(a, b)
	if err != nil || worst != 0 {
		t.Fatalf("worst=%s err=%v", worst, err)
	}
	b.Items[1].PTS += 700 * clock.Microsecond
	if worst, err = AlignmentError(a, b); err != nil || worst != 700*clock.Microsecond {
		t.Fatalf("worst=%s err=%v", worst, err)
	}
	b.Items = b.Items[:1]
	if _, err := AlignmentError(a, b); err == nil {
		t.Fatal("missing entry must be reported")
	}
}

func TestLegacyAndCurrentAgreeOnTimes(t *testing.T) {
	v1, _ := Decode([]byte(`{"version":1,"entries":[{"id":"v0","codec":"av1","time_ms":33,"duration_ms":33}]}`))
	v2, _ := Decode([]byte(`{"version":2,"tracks":[{"name":"video","timescale":90000}],"entries":[{"id":"v0","codec":"av1","track":"video","pts":2970,"duration":2970}]}`))
	worst, err := AlignmentError(v1, v2)
	if err != nil {
		t.Fatal(err)
	}
	if worst != 0 {
		t.Fatalf("v1/v2 disagree by %s", worst)
	}
}

func TestEncodeCurrentRoundTrip(t *testing.T) {
	m, err := Decode([]byte(currentJSON))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := EncodeCurrent(m, map[string]int64{"video": 90000, "audio": 48000})
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	worst, err := AlignmentError(m, back)
	if err != nil {
		t.Fatal(err)
	}
	if worst > clock.Microsecond {
		t.Fatalf("round trip error = %s", worst)
	}
}

func TestEncodeCurrentRequiresTimescale(t *testing.T) {
	m, _ := Decode([]byte(currentJSON))
	if _, err := EncodeCurrent(m, map[string]int64{"video": 90000}); err == nil {
		t.Fatal("missing audio timescale must fail")
	}
}
