package source

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIngestAndVerify(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "clip.mp4")
	payload := make([]byte, 128*1024)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := st.Ingest(context.Background(), src)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if a.Size != int64(len(payload)) {
		t.Fatalf("size = %d", a.Size)
	}
	if err := VerifyBlob(a.BlobPath, a.Hash); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Re-ingesting identical content under a new path reuses the same blob.
	src2 := filepath.Join(t.TempDir(), "copy.mp4")
	if err := os.WriteFile(src2, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := st.Ingest(context.Background(), src2)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if b.Hash != a.Hash || b.BlobPath != a.BlobPath {
		t.Fatalf("identical content must share one blob: %+v vs %+v", a, b)
	}
	// Tampering with the blob must fail verification.
	tampered := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(tampered, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlob(tampered, a.Hash); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestIngestPathReplacement(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	fifo := filepath.Join(work, "live.mp4")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(work, "new.mp4")
	finalPayload := make([]byte, 256*1024)
	for i := range finalPayload {
		finalPayload[i] = byte(i/13) ^ 0x5a
	}
	if err := os.WriteFile(replacement, finalPayload, 0o644); err != nil {
		t.Fatal(err)
	}
	wantHash, err := HashFile(replacement)
	if err != nil {
		t.Fatal(err)
	}

	// Writer holds the FIFO open and paces data so the replacement happens
	// while the reader is blocked inside a Read.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		_, _ = w.Write(make([]byte, readBufSize))
		time.Sleep(80 * time.Millisecond)
		_ = os.Rename(replacement, fifo) // replace directory entry mid-read
		_, _ = w.Write(make([]byte, readBufSize))
		time.Sleep(40 * time.Millisecond)
	}()

	a, err := st.Ingest(context.Background(), fifo)
	if err != nil {
		t.Fatalf("ingest with mid-read replacement: %v", err)
	}
	if a.Hash != wantHash {
		t.Fatalf("stale result published: got %s want %s", a.Hash, wantHash)
	}
	<-writerDone
}

func TestHashFileFreshPerPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "side.vtt")
	if err := os.WriteFile(p, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("same path with new content must produce a new hash")
	}
}
