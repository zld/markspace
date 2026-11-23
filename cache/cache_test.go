package cache

import (
	"testing"

	"mediacore/media"
)

func TestPutGetVerified(t *testing.T) {
	c := New()
	k := c.Put("src-a", []byte("hello"))
	got, err := c.Get("src-a", k)
	if err != nil || string(got) != "hello" {
		t.Fatalf("got %q err %v", got, err)
	}
	if c.Hits != 1 {
		t.Fatalf("hits=%d", c.Hits)
	}
}

func TestMiss(t *testing.T) {
	c := New()
	got, err := c.Get("src-a", Key([]byte("nope")))
	if err != nil || got != nil || c.Misses != 1 {
		t.Fatalf("got=%v err=%v misses=%d", got, err, c.Misses)
	}
}

func TestForgedSourceQuarantined(t *testing.T) {
	c := New()
	payload := []byte("result")
	k := Key(payload)
	// poisoned store: right content, wrong provenance
	c.Inject(k, Entry{Source: "attacker", Digest: k, Payload: payload})
	_, err := c.Get("src-a", k)
	if media.CodeOf(err) != media.CodeCacheSource {
		t.Fatalf("got %v", err)
	}
	if len(c.Quarantined()) != 1 {
		t.Fatal("entry not quarantined")
	}
	// entry is gone: recomputation path sees a clean miss
	if got, err := c.Get("src-a", k); err != nil || got != nil {
		t.Fatalf("after quarantine: got=%v err=%v", got, err)
	}
}

func TestForgedDigestQuarantined(t *testing.T) {
	c := New()
	k := c.Put("src-a", []byte("real"))
	// tamper with stored payload under the same key
	c.Inject(k, Entry{Source: "src-a", Digest: k, Payload: []byte("tampered")})
	_, err := c.Get("src-a", k)
	if media.CodeOf(err) != media.CodeCacheDigest {
		t.Fatalf("got %v", err)
	}
	if len(c.Quarantined()) != 1 {
		t.Fatal("entry not quarantined")
	}
}

func TestRecomputeAfterQuarantine(t *testing.T) {
	c := New()
	k := Key([]byte("v1"))
	c.Inject(k, Entry{Source: "evil", Digest: k, Payload: []byte("v1")})
	if _, err := c.Get("src-a", k); err == nil {
		t.Fatal("expected quarantine")
	}
	// caller recomputes and re-stores
	c.Put("src-a", []byte("v1"))
	got, err := c.Get("src-a", k)
	if err != nil || string(got) != "v1" {
		t.Fatalf("recompute path broken: %q %v", got, err)
	}
}
