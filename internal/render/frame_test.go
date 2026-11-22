package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/medialab/medialab/internal/clock"
)

func newTestRenderer(t *testing.T) (*Renderer, string) {
	t.Helper()
	dir := t.TempDir()
	return NewRenderer(Config{OutDir: dir, Width: FrameWidth, Height: FrameHeight}, FSWriter), dir
}

func TestDeterministicRasterAndHash(t *testing.T) {
	r1, _ := newTestRenderer(t)
	r2, _ := newTestRenderer(t)
	f1 := r1.Draw("v0", 12_345, 7, false)
	f2 := r2.Draw("v0", 12_345, 7, false)
	if f1.SHA256 != f2.SHA256 || f1.PixelDigest != f2.PixelDigest {
		t.Fatalf("repeatability broken: %+v vs %+v", f1, f2)
	}
	// The stored hash must match an independent recomputation of the bytes.
	raster := r1.Raster(f1)
	sum := sha256.Sum256(raster)
	if hex.EncodeToString(sum[:]) != f1.SHA256 {
		t.Fatal("frame SHA-256 does not match raster bytes")
	}
	if len(raster) != FrameWidth*FrameHeight {
		t.Fatalf("raster size = %d, want %d", len(raster), FrameWidth*FrameHeight)
	}
}

func TestDifferentInputsProduceDifferentFrames(t *testing.T) {
	r, _ := newTestRenderer(t)
	base := r.Draw("v0", 100, 1, false)
	cases := map[string]Frame{
		"time":    r.Draw("v0", 101, 1, false),
		"index":   r.Draw("v0", 100, 2, false),
		"waiting": r.Draw("v0", 100, 1, true),
	}
	for name, f := range cases {
		if f.SHA256 == base.SHA256 {
			t.Fatalf("frame for %s collides with base hash", name)
		}
	}
	if got := r.Draw("v0", 100, 1, true).Quality; got != 0.85 {
		t.Fatalf("degraded quality = %v, want 0.85", got)
	}
}

func TestWriteRoundTripAndSidecar(t *testing.T) {
	r, dir := newTestRenderer(t)
	f := r.Draw("v0", 5_000, 3, false)
	if err := r.Write(f, r.Raster(f)); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "frame_000003.bin")
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		t.Fatal("persisted raster hash mismatch")
	}
	meta, err := os.ReadFile(filepath.Join(dir, "frame_000003.json"))
	if err != nil {
		t.Fatal(err)
	}
	var side Frame
	if err := json.Unmarshal(meta, &side); err != nil {
		t.Fatal(err)
	}
	if side.ManifestMs != 5_000 || side.SHA256 != f.SHA256 {
		t.Fatalf("sidecar mismatch: %+v", side)
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk quota exceeded") }
func (failWriter) Close() error              { return nil }

func TestWriteFailureIsClassified(t *testing.T) {
	dir := t.TempDir()
	r := NewRenderer(Config{OutDir: dir}, func(path string) (io.WriteCloser, error) {
		return failWriter{}, nil
	})
	f := r.Draw("v0", 1, 0, false)
	err := r.Write(f, r.Raster(f))
	if err == nil {
		t.Fatal("write failure must surface")
	}
	me, ok := clock.As(err)
	if !ok || me.Kind != clock.KindRenderFailure || me.Track != "v0" || me.AtTick != 1 {
		t.Fatalf("render error missing location: %v", err)
	}
}

func TestOpenFailureIsClassified(t *testing.T) {
	r := NewRenderer(Config{OutDir: t.TempDir()}, func(path string) (io.WriteCloser, error) {
		return nil, errors.New("no such directory")
	})
	f := r.Draw("v0", 9, 0, false)
	if err := r.Write(f, r.Raster(f)); err == nil {
		t.Fatal("opener failure must surface")
	} else if me, ok := clock.As(err); !ok || me.Kind != clock.KindRenderFailure {
		t.Fatalf("unexpected error: %v", err)
	}
}
