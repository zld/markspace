package issuer

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestFirstVersionIsOne(t *testing.T) {
	d, err := New(Options{Now: fixedClock(time.Unix(0, 0))})
	if err != nil {
		t.Fatal(err)
	}
	cur := d.Current()
	if cur.ID != "k1" || cur.Version != 1 {
		t.Fatalf("first key is %s v%d, want k1 v1", cur.ID, cur.Version)
	}
	if d.Rotations() != 0 {
		t.Fatalf("rotation count is %d, want 0", d.Rotations())
	}
}

func TestRotateAdvancesVersionAndCount(t *testing.T) {
	d, err := New(Options{Now: fixedClock(time.Unix(0, 0))})
	if err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		k, rotations, err := d.Rotate()
		if err != nil {
			t.Fatal(err)
		}
		if rotations != want {
			t.Fatalf("rotations = %d, want %d", rotations, want)
		}
		if k.ID != KeyID(want+1) {
			t.Fatalf("active key = %s, want %s", k.ID, KeyID(want+1))
		}
	}
	if d.Rotations() != 3 {
		t.Fatalf("Rotations() = %d, want 3", d.Rotations())
	}
}

func TestRotationLimit(t *testing.T) {
	d, err := New(Options{Now: fixedClock(time.Unix(0, 0)), MaxRotations: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Rotate(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Rotate(); !errors.Is(err, ErrRotationLimit) {
		t.Fatalf("second rotation: %v, want ErrRotationLimit", err)
	}
}

func TestHistoryKeepsRetiredAndExpiredKeys(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	d, err := New(Options{Now: func() time.Time { return now }, KeyTTL: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	now = start.Add(5 * time.Second)
	if _, _, err := d.Rotate(); err != nil {
		t.Fatal(err)
	}
	hist := d.History()
	if len(hist) != 2 {
		t.Fatalf("history has %d entries, want 2", len(hist))
	}
	if hist[0].Active || !hist[1].Active {
		t.Fatalf("unexpected active flags: %+v", hist)
	}

	now = start.Add(20 * time.Second)
	if _, err := d.ResolveForDecrypt("k1", now); !errors.Is(err, ErrKeyExpired) {
		t.Fatalf("k1: %v, want ErrKeyExpired", err)
	}
	if _, err := d.ResolveForDecrypt("k2", now); !errors.Is(err, ErrKeyExpired) {
		t.Fatalf("k2: %v, want ErrKeyExpired", err)
	}
	if _, err := d.ResolveForDecrypt("k9", now); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("k9: %v, want ErrKeyNotFound", err)
	}
	if _, err := d.ResolveForEncrypt(now); !errors.Is(err, ErrKeyExpired) {
		t.Fatalf("encrypt with an expired current key: %v, want ErrKeyExpired", err)
	}
}

func TestResolveForDecryptBeforeExpiry(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d, err := New(Options{Now: fixedClock(start), KeyTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	k, err := d.ResolveForDecrypt("k1", start.Add(59*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if k.ID != "k1" {
		t.Fatalf("resolved %s, want k1", k.ID)
	}
	if _, err := d.ResolveForDecrypt("k1", start.Add(time.Minute)); !errors.Is(err, ErrKeyExpired) {
		t.Fatalf("at expiry boundary: %v, want ErrKeyExpired", err)
	}
}

func TestKeysAreDistinct(t *testing.T) {
	d, err := New(Options{Now: fixedClock(time.Unix(0, 0))})
	if err != nil {
		t.Fatal(err)
	}
	k1 := d.Current()
	k2, _, err := d.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	a1, err := k1.AEAD()
	if err != nil {
		t.Fatal(err)
	}
	a2, err := k2.AEAD()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, a1.NonceSize())
	ct := a1.Seal(nil, nonce, []byte("payload"), nil)
	if _, err := a2.Open(nil, nonce, ct, nil); err == nil {
		t.Fatal("a rotated key decrypted ciphertext from a different version")
	}
}

func TestConcurrentRotationIsSerialized(t *testing.T) {
	d, err := New(Options{Now: fixedClock(time.Unix(0, 0)), MaxRotations: 50})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = d.Rotate()
		}()
	}
	wg.Wait()
	hist := d.History()
	seen := map[string]bool{}
	for _, h := range hist {
		if seen[h.ID] {
			t.Fatalf("duplicate key id %s", h.ID)
		}
		seen[h.ID] = true
		if !strings.HasPrefix(h.ID, "k") {
			t.Fatalf("unexpected key id %q", h.ID)
		}
	}
	if d.Rotations() != len(hist)-1 {
		t.Fatalf("rotations = %d, history = %d", d.Rotations(), len(hist))
	}
}
