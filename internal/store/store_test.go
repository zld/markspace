package store

import (
	"path/filepath"
	"testing"
)

type params struct {
	Threshold int `json:"threshold"`
}

func TestSourceDigestStableAndImmutableCopy(t *testing.T) {
	data := []byte("source asset")
	src := SourceFromBytes(data, "x.mp4")
	if src.Digest != DigestBytes(data) || src.Size != int64(len(data)) {
		t.Fatal("source summary mismatch")
	}
	data[0] = 'X'
	if got := string(src.Bytes()); got != "source asset" {
		t.Fatalf("source changed through caller slice: %q", got)
	}
	out := src.Bytes()
	out[0] = 'Y'
	if got := string(src.Bytes()); got != "source asset" {
		t.Fatal("Bytes must return a defensive copy")
	}
}

func TestDerivedKeyDistinguishesOnlyRelevantParams(t *testing.T) {
	source := DigestBytes([]byte("a"))
	seg1, err := NewKey("segment.cut", source, params{Threshold: 1})
	if err != nil {
		t.Fatal(err)
	}
	seg2, _ := NewKey("segment.cut", source, params{Threshold: 1})
	audio1, _ := NewKey("audio.silence", source, params{Threshold: 1})
	audio2, _ := NewKey("audio.silence", source, params{Threshold: 2})
	if seg1.Key != seg2.Key {
		t.Fatal("identical params must yield identical key")
	}
	if audio1.Key == audio2.Key {
		t.Fatal("parameter change must invalidate derived key")
	}
	otherSource, _ := NewKey("segment.cut", DigestBytes([]byte("b")), params{Threshold: 1})
	if seg1.Key == otherSource.Key {
		t.Fatal("source digest must participate in key")
	}
	if seg1.ModuleVersion == "" || seg1.ParamsDigest == "" {
		t.Fatalf("key components missing: %+v", seg1)
	}
}

func TestJSONCacheRoundTrip(t *testing.T) {
	cache, err := NewCache(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewKey("audio.silence", "src", params{Threshold: 9})
	value := map[string]any{"ok": true, "n": 3}
	if err := cache.StoreJSON(key, value); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	hit, err := cache.LoadJSON(key, &got)
	if err != nil || !hit {
		t.Fatalf("hit=%v err=%v", hit, err)
	}
	if got["ok"] != true || got["n"] != float64(3) {
		t.Fatalf("cached = %#v", got)
	}
}
