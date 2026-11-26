package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSHA256StreamingAndParts(t *testing.T) {
	want := SHA256Bytes([]byte("media-pipeline"))
	got, n, err := SHA256Reader(strings.NewReader("media-pipeline"))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len("media-pipeline")) || got != want {
		t.Fatalf("reader hash/size = %q,%d want %q", got, n, want)
	}
	if HashParts([]byte("a"), []byte("b")) == HashParts([]byte("ab")) {
		t.Fatal("length-prefixed part hashes must not collide")
	}
}

func TestSHA256File(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, size, err := SHA256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if size != 3 || got != SHA256Bytes([]byte("abc")) {
		t.Fatalf("file hash/size = %q,%d", got, size)
	}
}

func TestHashJSONDeterministic(t *testing.T) {
	a, err := HashJSON(struct {
		B int    `json:"b"`
		A string `json:"a"`
	}{B: 2, A: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashJSON(struct {
		B int    `json:"b"`
		A string `json:"a"`
	}{B: 2, A: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("same value produced different JSON hash")
	}
}
