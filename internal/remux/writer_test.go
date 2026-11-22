package remux

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/demux"
	"mediasync/internal/testmedia"
)

func buildMapped(t *testing.T) (*demux.Mapping, []*demux.Track, int64, []Sample) {
	t.Helper()
	data, truth := testmedia.BuildWebM(testmedia.Spec{
		Seed: 41, Tracks: []string{"video", "audio", "subtitle"},
		Clusters: 4, BlocksPerCluster: 3,
	})
	d, err := demux.New(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tracks := d.Tracks()
	mapping, err := demux.Resolve(tracks, []demux.Binding{
		{Source: 1, Role: demux.RoleVideo},
		{Source: 2, Role: demux.RoleAudio},
		{Source: 3, Role: demux.RoleSubtitle},
	})
	if err != nil {
		t.Fatal(err)
	}
	var samples []Sample
	for {
		s, err := d.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, Sample{
			Track: s.Track, Seq: s.Seq, NS: s.TimeTicks * 1_000_000, Payload: s.Payload,
		})
	}
	if len(samples) != len(truth.Blocks) {
		t.Fatalf("samples %d want %d", len(samples), len(truth.Blocks))
	}
	return mapping, tracks, d.ScaleNS(), samples
}

func TestLosslessRoundTrip(t *testing.T) {
	mapping, tracks, scale, samples := buildMapped(t)
	w, err := NewWriter(mapping, tracks, scale)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	out, err := w.WriteTo(&buf, samples)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Published || out.Samples != len(samples) {
		t.Fatalf("outcome: %+v", out)
	}
	// Re-demux the output: every payload must survive byte-for-byte.
	d2, err := demux.New(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("output not parseable: %v", err)
	}
	idx := 0
	for {
		s, err := d2.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("re-demux: %v", err)
		}
		if !bytes.Equal(s.Payload, samples[idx].Payload) {
			t.Fatalf("payload %d altered by remux", idx)
		}
		idx++
	}
	if idx != len(samples) {
		t.Fatalf("re-demuxed %d of %d", idx, len(samples))
	}
}

func TestDeterministicDigestAcrossReruns(t *testing.T) {
	mapping, tracks, scale, samples := buildMapped(t)
	render := func() ([]byte, [32]byte) {
		w, _ := NewWriter(mapping, tracks, scale)
		var buf bytes.Buffer
		out, err := w.WriteTo(&buf, samples)
		if err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), out.PayloadHash
	}
	a, ha := render()
	b, hb := render()
	if !bytes.Equal(a, b) {
		t.Fatal("container bytes differ across reruns")
	}
	if ha != hb {
		t.Fatal("payload digest differs across reruns")
	}
}

func TestAtomicPublishLeavesNoPartialOutput(t *testing.T) {
	mapping, tracks, scale, samples := buildMapped(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.webm")
	w, err := NewWriter(mapping, tracks, scale)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Publish(target, samples)
	if err != nil || !out.Published {
		t.Fatalf("publish: %v %+v", err, out)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target missing: %v", err)
	}
	// No temp files may remain after a successful atomic publish.
	matches, _ := filepath.Glob(filepath.Join(dir, ".remux-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temporary files leaked: %v", matches)
	}
}

func TestRejectsOversizeOutput(t *testing.T) {
	mapping, tracks, scale, samples := buildMapped(t)
	w, _ := NewWriter(mapping, tracks, scale)
	big := make([]byte, MaxOutputBytes+1)
	huge := append([]Sample(nil), samples...)
	huge[0] = Sample{Track: 1, NS: 0, Payload: big}
	var buf bytes.Buffer
	if _, err := w.WriteTo(&buf, huge); err == nil {
		t.Fatal("oversize output must be rejected before publication")
	}
}

func TestMP4SidecarStructurallyValid(t *testing.T) {
	mapping, tracks, _, samples := buildMapped(t)
	videoTrack := tracks[0]
	if videoTrack.Type != demux.TypeVideo {
		t.Fatalf("first track type = %d", videoTrack.Type)
	}
	_ = mapping
	var buf bytes.Buffer
	seeker := newBufSeeker()
	if err := WriteMP4Sidecar(seeker, videoTrack, samples); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	buf.Write(seeker.Bytes())
	n, hasMdat, err := VerifyMP4(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("verify sidecar: %v", err)
	}
	// A valid sidecar must contain mdat and a fully populated moov subtree
	// (mvhd, trak, mdia, minf, stbl and its child boxes), so the recursive
	// count is well above the three top-level boxes alone.
	if !hasMdat || n < 10 {
		t.Fatalf("sidecar structurally incomplete: boxes=%d mdat=%v", n, hasMdat)
	}
}

func TestNilMappingFailsBeforeFile(t *testing.T) {
	if _, err := NewWriter(nil, nil, 1_000_000); err == nil {
		t.Fatal("nil mapping must fail")
	}
}
