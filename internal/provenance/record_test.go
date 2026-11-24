package provenance

import (
	"encoding/json"
	"testing"
)

func TestAddKeepsSortedAndDeduped(t *testing.T) {
	r := NewRecorder("run", 7)
	r.Add(Entry{Node: "mux", InputDigest: "i3"})
	r.Add(Entry{Node: "parse", InputDigest: "i1"})
	r.Add(Entry{Node: "jitter", InputDigest: "i2"})
	r.Add(Entry{Node: "parse", InputDigest: "i1-updated"})

	got := r.Entries()
	want := []string{"jitter", "mux", "parse"}
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3 (dedup)", len(got))
	}
	for i, e := range got {
		if e.Node != want[i] {
			t.Fatalf("entry %d = %s, want %s (sorted)", i, e.Node, want[i])
		}
	}
	if got[2].InputDigest != "i1-updated" {
		t.Fatal("re-added node must replace its entry")
	}
	for _, e := range got {
		if e.NodeVersion != NodeVersion {
			t.Fatalf("node version not defaulted: %+v", e)
		}
	}
}

func TestCanonicalDigestStableAcrossRetries(t *testing.T) {
	build := func() string {
		r := NewRecorder("run", 7)
		r.Add(Entry{Node: "b", InputDigest: "ib", ConfigDigest: "cb", CheckpointState: "done", OutputDigest: "ob"})
		r.Add(Entry{Node: "a", InputDigest: "ia", ConfigDigest: "ca", CheckpointState: "done", OutputDigest: "oa"})
		d, err := r.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if build() != build() {
		t.Fatal("digest must be identical for equivalent records")
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	r := NewRecorder("run", 1)
	r.Add(Entry{Node: "parse", InputDigest: "i", ConfigDigest: "c", CheckpointState: "done", OutputDigest: "o"})
	raw, err := r.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	d, _ := r.Digest()
	if !Verify(raw, d) {
		t.Fatal("Verify must accept the canonical document")
	}
	if Verify(raw, "deadbeef") {
		t.Fatal("Verify must reject a wrong digest")
	}
}

func TestRecordIsValidJSON(t *testing.T) {
	r := NewRecorder("run", 3)
	r.Add(Entry{Node: "x", InputDigest: "i"})
	raw, err := r.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	if back["run_id"] != "run" || back["seed"].(float64) != 3 {
		t.Fatalf("unexpected JSON: %s", raw)
	}
}

func TestDigestBytes(t *testing.T) {
	if DigestBytes(nil) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("empty-input SHA-256 mismatch")
	}
}
