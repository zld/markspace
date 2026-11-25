package checkpoint

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type payload struct {
	N     int      `json:"n"`
	Items []string `json:"items"`
}

func TestWriteLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	in := payload{N: 3, Items: []string{"a", "b"}}
	if err := s.Write("task", "scan", "digest", in); err != nil {
		t.Fatal(err)
	}
	var out payload
	if err := s.Load("task", "scan", "digest", &out); err != nil {
		t.Fatal(err)
	}
	if out.N != 3 || len(out.Items) != 2 {
		t.Fatalf("round trip = %+v", out)
	}
}

func TestStaleDigestRefused(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Write("task", "scan", "digest-A", payload{N: 1}); err != nil {
		t.Fatal(err)
	}
	var out payload
	err := s.Load("task", "scan", "digest-B", &out)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestPayloadTamperDetected(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Write("task", "scan", "d", payload{N: 1}); err != nil {
		t.Fatal(err)
	}
	path := s.Path("task", "scan")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc Doc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Payload = json.RawMessage(`{"n":99,"items":null}`)
	bad, _ := json.MarshalIndent(&doc, "", "  ")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("task", "scan"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestMissing(t *testing.T) {
	s := New(t.TempDir())
	if s.Has("t", "cut") {
		t.Fatal("Has should be false")
	}
	if _, err := s.Read("t", "cut"); !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v, want ErrMissing", err)
	}
	if err := s.Remove("t", "cut"); err != nil {
		t.Fatalf("remove missing should be nil: %v", err)
	}
}

func TestAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	for i := 0; i < 5; i++ {
		if err := s.Write("task", "scan", "d", payload{N: i}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want 1", len(entries))
	}
}

func TestSanitizedPath(t *testing.T) {
	s := New("/tmp/x")
	p := s.Path("task/with:odd chars", "scan")
	for _, bad := range []string{"/", ":", " "} {
		if contains(p, bad) && bad != "/" {
			t.Fatalf("path %q contains %q", p, bad)
		}
	}
	if !contains(p, "scan.ckpt.json") {
		t.Fatalf("path %q missing stage suffix", p)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
