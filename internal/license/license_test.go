package license

import (
	"errors"
	"strings"
	"testing"
	"time"

	"mediakey/internal/reason"
)

var testSecret = []byte("0123456789abcdef-test-secret")

func newTestSigner(t *testing.T, at time.Time) *Signer {
	t.Helper()
	s, err := NewSigner(Options{Secret: testSecret, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIssueAndCheckRoundTrip(t *testing.T) {
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset-7", 1, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l.Serial, "lic-") || len(l.Serial) != len("lic-")+32 {
		t.Fatalf("unexpected serial %q", l.Serial)
	}
	if err := l.Check(testSecret, start.Add(30*time.Second), "asset-7"); err != nil {
		t.Fatalf("valid license rejected: %v", err)
	}
}

func TestSerialIsUnique(t *testing.T) {
	start := time.Unix(0, 0)
	s := newTestSigner(t, start)
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		l, err := s.Issue("asset", 1, 1, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if seen[l.Serial] {
			t.Fatalf("duplicate serial %q", l.Serial)
		}
		seen[l.Serial] = true
	}
}

func TestSignatureCoversEveryField(t *testing.T) {
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset-7", 1, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*License){
		"asset":   func(x *License) { x.AssetID = "asset-8" },
		"key_min": func(x *License) { x.KeyMin = 2 },
		"key_max": func(x *License) { x.KeyMax = 9 },
		"issued":  func(x *License) { x.IssuedAt = start.Add(-time.Hour).Format(time.RFC3339Nano) },
		"expires": func(x *License) { x.ExpiresAt = start.Add(time.Hour).Format(time.RFC3339Nano) },
		"serial":  func(x *License) { x.Serial = "lic-deadbeef" },
	}
	for name, mutate := range mutations {
		clone := *l
		mutate(&clone)
		err := clone.Check(testSecret, start.Add(time.Second), "asset-7")
		if !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%s: %v, want ErrBadSignature", name, err)
		}
		if code := ReasonCode(err); code != reason.LICENSE_SIGNATURE {
			t.Fatalf("%s: reason %s, want LICENSE_SIGNATURE", name, code)
		}
	}
}

func TestWrongSecretRejected(t *testing.T) {
	start := time.Unix(0, 0)
	s := newTestSigner(t, start)
	l, err := s.Issue("asset", 1, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Check([]byte("another-secret-value-16b"), start, "asset"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("got %v, want ErrBadSignature", err)
	}
}
