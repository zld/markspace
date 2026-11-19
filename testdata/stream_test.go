package generator

import (
	"bytes"
	"io"
	"testing"
)

func TestDeterministicStreamIsStableAndChunkIndependent(t *testing.T) {
	a := NewDeterministicStream(5, "keys")
	b := NewDeterministicStream(5, "keys")
	whole := make([]byte, 96)
	if _, err := io.ReadFull(a, whole); err != nil {
		t.Fatal(err)
	}
	var chunked bytes.Buffer
	buf := make([]byte, 7)
	for chunked.Len() < len(whole) {
		n, err := b.Read(buf)
		remaining := len(whole) - chunked.Len()
		if n > remaining {
			n = remaining
		}
		chunked.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(whole, chunked.Bytes()) {
		t.Fatal("stream output depends on read chunk size")
	}
}

func TestDeterministicStreamDomainSeparation(t *testing.T) {
	a := NewDeterministicStream(5, "keys")
	b := NewDeterministicStream(5, "nonces")
	c := NewDeterministicStream(6, "keys")
	x := make([]byte, 32)
	y := make([]byte, 32)
	z := make([]byte, 32)
	if _, err := io.ReadFull(a, x); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(b, y); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, z); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(x, y) || bytes.Equal(x, z) {
		t.Fatal("streams for different labels or seeds collided")
	}
}

func TestDeterministicStreamDoesNotRepeat(t *testing.T) {
	s := NewDeterministicStream(1, "keys")
	buf := make([]byte, 64)
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(buf[:32], buf[32:]) {
		t.Fatal("adjacent stream blocks are identical")
	}
}
