package store

import "testing"

func TestDerivedCacheHits(t *testing.T) {
	d := NewDerived(t.TempDir())
	key := Key("source:abc", "track:def", "policy:ghi")
	if _, ok, _ := d.Get(key); ok {
		t.Fatal("empty cache reported a hit")
	}
	if err := d.Put(key, map[string]int{"segments": 3}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.Get(key); !ok || err != nil {
		t.Fatalf("expected hit, got %v %v", ok, err)
	}
	if st := d.Stats(); st.Hits != 1 || st.Misses != 1 || st.Written != 1 {
		t.Fatalf("bad stats %+v", st)
	}
	// A different source digest is a different key.
	if _, ok, _ := d.Get(Key("source:xyz", "track:def", "policy:ghi")); ok {
		t.Fatal("changed source must miss")
	}
}

func TestDerivedRejectsBadKey(t *testing.T) {
	d := NewDerived(t.TempDir())
	if _, _, err := d.Get("../etc/passwd"); err == nil {
		t.Fatal("expected invalid key error")
	}
}
