// Package remux writes the lossless delivery package. Codec payloads are
// copied byte-for-byte and never decoded; the writer only rebuilds container
// structure with known element sizes so identical inputs produce
// byte-identical outputs across runs.
package remux

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"mediasync/internal/demux"
	"mediasync/internal/ebml"
)

// MaxOutputBytes bounds a single delivery package, mirroring the input cap
// so a corrupt size cannot drive an unbounded write.
const MaxOutputBytes = 512 << 20

// Outcome summarizes what was (or was not) published.
type Outcome struct {
	Path        string
	Bytes       int64
	Clusters    int
	Samples     int
	Tracks      int
	Published   bool
	PayloadHash [32]byte // SHA-256 over every emitted payload in merge order
}

// Writer rebuilds a WebM container from mapped tracks and merged samples.
type Writer struct {
	tracks  []*demux.Track
	mapping *demux.Mapping
	scaleNS int64
}

// NewWriter validates that every mapped source still has header metadata and
// prepares a writer. Failures here happen before any output file is created.
func NewWriter(mapping *demux.Mapping, tracks []*demux.Track, scaleNS int64) (*Writer, error) {
	if mapping == nil {
		return nil, errors.New("remux: nil mapping")
	}
	if scaleNS <= 0 {
		return nil, fmt.Errorf("remux: invalid timecode scale %d", scaleNS)
	}
	known := map[uint32]bool{}
	for _, tr := range tracks {
		known[tr.Number] = true
	}
	for _, src := range mapping.Sources() {
		if !known[src] {
			return nil, fmt.Errorf("remux: mapped source %d missing track header", src)
		}
	}
	return &Writer{tracks: tracks, mapping: mapping, scaleNS: scaleNS}, nil
}

// mergedSample is the writer's view of one pipeline result.
type mergedSample struct {
	Track   uint32
	Seq     uint64
	NS      int64
	Payload []byte
}

// render serializes the full container to a byte slice. Keeping rendering
// separate from file publication lets the caller verify size and hashing
// before the atomic rename, so a failure never leaves a partial package.
func (w *Writer) render(samples []mergedSample) ([]byte, [32]byte, int, error) {
	var out []byte
	out = append(out, buildEBMLHeader()...)
	segBody := buildInfo(w.scaleNS)
	segBody = append(segBody, w.buildTracks()...)

	clusters, n, hash, err := w.buildClusters(samples)
	if err != nil {
		return nil, hash, 0, err
	}
	segBody = append(segBody, clusters...)
	if int64(len(segBody)) > MaxOutputBytes {
		return nil, hash, 0, fmt.Errorf("remux: output %d exceeds limit %d", len(segBody), MaxOutputBytes)
	}
	out = append(out, ebml.EncodeElement(ebml.IDSegment, segBody)...)
	return out, hash, n, nil
}

// Publish atomically writes the rendered package: bytes go to a temp file in
// the target directory and are renamed into place only after a complete,
// verified write. A crash mid-write therefore leaves no half-finished output.
func (w *Writer) Publish(filePath string, samples []mergedSample) (Outcome, error) {
	data, hash, n, err := w.render(samples)
	if err != nil {
		return Outcome{Published: false}, err
	}
	dir := path.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".remux-*.tmp")
	if err != nil {
		return Outcome{Published: false}, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return Outcome{Published: false}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return Outcome{Published: false}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return Outcome{Published: false}, err
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		cleanup()
		return Outcome{Published: false}, err
	}
	return Outcome{
		Path: filePath, Bytes: int64(len(data)), Clusters: clusterCount(data),
		Samples: n, Tracks: len(w.mapping.Sources()), Published: true, PayloadHash: hash,
	}, nil
}

// WriteTo renders and streams the package without atomic publication, used by
// tests that want to inspect bytes directly.
func (w *Writer) WriteTo(out io.Writer, samples []mergedSample) (Outcome, error) {
	data, hash, n, err := w.render(samples)
	if err != nil {
		return Outcome{Published: false}, err
	}
	if _, err := out.Write(data); err != nil {
		return Outcome{Published: false}, err
	}
	return Outcome{
		Bytes: int64(len(data)), Clusters: clusterCount(data),
		Samples: n, Tracks: len(w.mapping.Sources()), Published: true, PayloadHash: hash,
	}, nil
}

func (w *Writer) buildTracks() []byte {
	tb := &ebml.MasterBuilder{}
	for _, n := range w.mapping.Sources() {
		tr, _ := w.mapping.Track(n)
		entry := &ebml.MasterBuilder{}
		entry.Uint(ebml.IDTrackNumber, uint64(tr.Number))
		entry.Uint(ebml.IDTrackUID, tr.UID)
		entry.Uint(ebml.IDTrackType, tr.Type)
		entry.String(ebml.IDCodecID, tr.CodecID)
		entry.Uint(ebml.IDFlagLacing, 0)
		if len(tr.CodecPrivate) > 0 {
			entry.Bytes(ebml.IDCodecPrivate, tr.CodecPrivate)
		}
		if tr.Type == demux.TypeVideo {
			vid := &ebml.MasterBuilder{}
			vid.Uint(ebml.IDPixelWidth, tr.VideoWidth)
			vid.Uint(ebml.IDPixelHeight, tr.VideoHeight)
			entry.Master(ebml.IDVideo, vid)
		}
		if tr.Type == demux.TypeAudio {
			aud := &ebml.MasterBuilder{}
			aud.Float(ebml.IDSamplingFreq, tr.AudioFreq)
			aud.Uint(ebml.IDChannels, tr.AudioChannels)
			entry.Master(ebml.IDAudio, aud)
		}
		tb.Master(ebml.IDTrackEntry, entry)
	}
	return ebml.Build(ebml.IDTracks, tb)
}

// buildClusters groups merged samples into clusters by whole 500 ms windows,
// keeping each cluster's signed 16-bit relative timecode safely in range.
func (w *Writer) buildClusters(samples []mergedSample) ([]byte, int, [32]byte, error) {
	hasher := newPayloadHasher()
	const windowNS = int64(500_000_000)
	var out []byte
	clusterCount := 0
	var window []mergedSample

	flush := func() error {
		if len(window) == 0 {
			return nil
		}
		base := window[0].NS / windowNS * windowNS
		cb := &ebml.MasterBuilder{}
		// Cluster timecode is expressed in the container's tick scale.
		cb.Uint(ebml.IDTimecode, uint64(nsToTicks(base, w.scaleNS)))
		for _, s := range window {
			rel := nsToTicks(s.NS-base, w.scaleNS)
			if rel < -32768 || rel > 32767 {
				return fmt.Errorf("remux: relative timecode %d out of int16 range", rel)
			}
			cb.Write(encodeSimpleBlock(s.Track, rel, s.Payload))
			hasher.write(s.Payload)
		}
		out = append(out, ebml.Build(ebml.IDCluster, cb)...)
		clusterCount++
		window = nil
		return nil
	}

	for _, s := range samples {
		if len(window) > 0 {
			curWindow := s.NS / windowNS
			baseWindow := window[0].NS / windowNS
			if curWindow != baseWindow {
				if err := flush(); err != nil {
					return nil, 0, hasher.sum(), err
				}
			}
		}
		window = append(window, s)
	}
	if err := flush(); err != nil {
		return nil, 0, hasher.sum(), err
	}
	return out, len(samples), hasher.sum(), nil
}

func nsToTicks(ns, scaleNS int64) int64 {
	return ns / scaleNS
}

func clusterCount(data []byte) int {
	id := ebml.EncodeID(ebml.IDCluster)
	n := 0
	for i := 0; i+len(id) <= len(data); i++ {
		match := true
		for j := range id {
			if data[i+j] != id[j] {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

func buildEBMLHeader() []byte {
	hb := &ebml.MasterBuilder{}
	hb.Uint(0x4286, 1)
	hb.Uint(0x42F7, 1)
	hb.Uint(0x42F2, 4)
	hb.Uint(0x42F3, 8)
	hb.String(0x4282, "webm")
	hb.Uint(0x4287, 2)
	hb.Uint(0x4285, 2)
	return ebml.Build(ebml.IDEBMLExtra, hb)
}

func buildInfo(scaleNS int64) []byte {
	ib := &ebml.MasterBuilder{}
	ib.Uint(ebml.IDTimecodeScale, uint64(scaleNS))
	ib.String(ebml.IDMuxingApp, "mediasync")
	ib.String(ebml.IDWritingApp, "mediasync")
	return ebml.Build(ebml.IDInfo, ib)
}

func encodeSimpleBlock(track uint32, relTicks int64, payload []byte) []byte {
	header := []byte{0x80 | byte(track)}
	header = append(header, byte(relTicks>>8), byte(relTicks))
	body := append(append(header, 0), payload...)
	return ebml.EncodeElement(ebml.IDSimpleBlock, body)
}
