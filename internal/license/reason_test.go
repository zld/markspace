package license

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"mediakey/internal/reason"
)

func TestCheckFailureOrder(t *testing.T) {
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset-7", 1, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Check(testSecret, start, "other-asset"); !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("asset mismatch: %v", err)
	}
	if err := l.Check(testSecret, start.Add(time.Minute), "asset-7"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry boundary: %v", err)
	}
	if err := l.Check(testSecret, start.Add(time.Hour), "asset-7"); ReasonCode(err) != reason.LICENSE_EXPIRED {
		t.Fatalf("expired reason: %v", ReasonCode(err))
	}

	// A missing field wins over a bad signature: the license is structurally
	// incomplete, which is the more actionable diagnosis.
	broken := *l
	broken.Serial = ""
	if err := broken.Check(testSecret, start, "asset-7"); ReasonCode(err) != reason.LICENSE_MISSING_FIELD {
		t.Fatalf("missing field reason: %v", ReasonCode(err))
	}
}

func TestNilLicense(t *testing.T) {
	var l *License
	if err := l.Check(testSecret, time.Now(), "a"); ReasonCode(err) != reason.LICENSE_MISSING_FIELD {
		t.Fatalf("nil license: %v", ReasonCode(err))
	}
}

func TestKeyRange(t *testing.T) {
	start := time.Unix(0, 0)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset", 2, 4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{2, 3, 4} {
		if err := l.CheckKeyRange(v); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
	for _, v := range []int{1, 5} {
		if err := l.CheckKeyRange(v); ReasonCode(err) != reason.KEY_OUT_OF_RANGE {
			t.Fatalf("version %d: %v", v, ReasonCode(err))
		}
	}
}

func TestEncodingRoundTrip(t *testing.T) {
	start := time.Unix(0, 0)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset", 1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(l)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(l)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again) {
		t.Fatal("license encoding is not deterministic")
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Check(testSecret, start, "asset"); err != nil {
		t.Fatalf("decoded license rejected: %v", err)
	}
}

func TestMalformedPayloadReasons(t *testing.T) {
	start := time.Unix(0, 0)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset", 1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*License){
		"version":   func(x *License) { x.Version = 42 },
		"bad_range": func(x *License) { x.KeyMin, x.KeyMax = 5, 1 },
		"bad_date":  func(x *License) { x.IssuedAt = "yesterday" },
		"order":     func(x *License) { x.ExpiresAt = x.IssuedAt },
	} {
		clone := *l
		mutate(&clone)
		code := ReasonCode(clone.Check(testSecret, start, "asset"))
		if code != reason.LICENSE_INVALID {
			t.Fatalf("%s: reason %s, want LICENSE_INVALID", name, code)
		}
	}
}

func TestSignerRejectsWeakSecret(t *testing.T) {
	if _, err := NewSigner(Options{Secret: []byte("short"), Now: time.Now}); !errors.Is(err, ErrWeakSecret) {
		t.Fatalf("got %v, want ErrWeakSecret", err)
	}
}

func TestIssueRejectsBadInputs(t *testing.T) {
	s := newTestSigner(t, time.Unix(0, 0))
	if _, err := s.Issue("", 1, 1, time.Minute); err == nil {
		t.Fatal("empty asset id accepted")
	}
	if _, err := s.Issue("a", 0, 1, time.Minute); err == nil {
		t.Fatal("zero key min accepted")
	}
	if _, err := s.Issue("a", 3, 1, time.Minute); err == nil {
		t.Fatal("inverted key range accepted")
	}
	if _, err := s.Issue("a", 1, 1, 0); err == nil {
		t.Fatal("zero validity accepted")
	}
}
