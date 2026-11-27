// Package remux writes mapped, compressed samples into a deterministic
// container without re-encoding. Every output sample keeps its exact bytes and
// is paired with full source/target offset provenance. Outputs are staged in a
// temporary file and atomically renamed only after byte-level verification, so
// a failure never leaves a half-written deliverable.
package remux

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediapack/internal/binio"
	"mediapack/internal/maptrack"
	"mediapack/internal/model"
)

// Magic and box types of the minimal deterministic container.
const (
	Magic     = "MPK1"
	boxHeader = "mpkh"
	boxTrack  = "trak"
	boxSample = "smp1"
)

// SampleRecord is the per-sample provenance row in the output manifest.
type SampleRecord struct {
	Source       string `json:"source"`
	SourceTrack  uint32 `json:"sourceTrack"`
	TargetTrack  uint32 `json:"targetTrack"`
	Index        uint32 `json:"index"`
	SrcOffset    int64  `json:"srcOffset"`
	TargetOffset int64  `json:"targetOffset"`
	Size         uint32 `json:"size"`
}

// Manifest is the verifiable evidence of one remux run.
type Manifest struct {
	Output      string         `json:"output"`
	Records     []SampleRecord `json:"records"`
	SampleCount int            `json:"sampleCount"`
	ByteDiff    int64          `json:"byteDiff"`
	TrackCount  int            `json:"trackCount"`
}

// posWriter tracks the absolute byte position while buffering a box stream.
type posWriter struct {
	buf bytes.Buffer
	pos int64
}

func (w *posWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	w.pos += int64(n)
	return n, err
}

func (w *posWriter) Seek(_ int64, _ int) (int64, error) { return w.pos, nil }

// Write serializes the mapping to outPath, verifies every payload byte against
// the source samples and atomically commits on success. ByteDiff is zero on a
// clean run; any mismatch returns CodeVerify and removes the staged file.
func Write(m *maptrack.Mapping, outPath string) (*Manifest, error) {
	if m == nil || len(m.Bindings) == 0 {
		return nil, model.NewError(model.CodeMalformedContainer,
			"nothing to remux: empty mapping")
	}
	staged := outPath + ".tmp"
	manifest, err := build(m, staged)
	if err != nil {
		_ = os.Remove(staged)
		return nil, err
	}
	// Re-open the staged file and verify byte-for-byte before commit.
	diff, err := verify(staged, m, manifest)
	if err != nil {
		_ = os.Remove(staged)
		return nil, err
	}
	manifest.ByteDiff = diff
	if diff != 0 {
		_ = os.Remove(staged)
		return manifest, model.NewError(model.CodeVerify,
			fmt.Sprintf("output verification failed: %d differing bytes", diff)).
			WithFile(staged)
	}
	if err := os.Rename(staged, outPath); err != nil {
		_ = os.Remove(staged)
		return nil, model.NewError(model.CodeVerify,
			"cannot commit output: "+err.Error()).WithFile(outPath)
	}
	manifest.Output = outPath
	return manifest, nil
}

// build lays out the container in deterministic target-track order and returns
// the manifest with resolved target offsets.
func build(m *maptrack.Mapping, staged string) (*Manifest, error) {
	w := &posWriter{}
	header := append([]byte(Magic), binio.BE32(uint32(len(m.Bindings)))...)
	if _, _, err := binio.Box(w, boxHeader, header); err != nil {
		return nil, err
	}
	mf := &Manifest{Output: staged, TrackCount: len(m.Bindings)}

	for _, b := range m.Bindings {
		trak := new(bytes.Buffer)
		trak.Write(binio.BE32(b.TargetTrack))
		writeStr(trak, b.SourcePath)
		trak.Write(binio.BE32(b.SourceTrack))
		writeStr(trak, b.Codec)
		trak.Write(binio.BE32(uint32(len(b.Samples))))

		// Each smp1 box is buffered inside this trak. We record, relative to
		// the trak body, the smp1 box start, the target-offset placeholder and
		// the payload start. Absolute positions are resolved once the trak box
		// start is known, so no smp1 header is double-counted.
		type slot struct {
			rec        SampleRecord
			patchRel   int // target-offset field offset within trak body
			payloadRel int // payload offset within trak body
		}
		var slots []slot
		for _, s := range b.Samples {
			rec := SampleRecord{
				Source: s.Source, SourceTrack: s.SourceTrack,
				TargetTrack: b.TargetTrack, Index: s.Index,
				SrcOffset: s.SrcOffset, Size: s.Size,
			}
			body := new(bytes.Buffer)
			body.Write(binio.BE32(rec.Index))
			body.Write(binio.BE32(rec.SourceTrack))
			body.Write(binio.BE32(rec.TargetTrack))
			writeStr(body, rec.Source)
			body.Write(binio.BE64(uint64(rec.SrcOffset)))
			patchInBody := body.Len()
			body.Write(make([]byte, 8)) // target offset placeholder
			body.Write(binio.BE32(rec.Size))
			payloadInBody := body.Len()

			smpStart := trak.Len() // relative to trak body, before the box
			trak.Write(boxBytesLocal(boxSample, append(body.Bytes(), s.Payload...)))
			slots = append(slots, slot{
				rec:        rec,
				patchRel:   smpStart + 8 + patchInBody,
				payloadRel: smpStart + 8 + payloadInBody,
			})
		}

		// Resolve absolute target offsets and patch placeholders before the
		// trak box is framed and written.
		trakStart := w.pos
		trakHeaderSize := int64(8)
		for _, sl := range slots {
			sl.rec.TargetOffset = trakStart + trakHeaderSize + int64(sl.payloadRel)
			binary.BigEndian.PutUint64(trak.Bytes()[sl.patchRel:sl.patchRel+8],
				uint64(sl.rec.TargetOffset))
			mf.Records = append(mf.Records, sl.rec)
		}
		if _, _, err := binio.Box(w, boxTrack, trak.Bytes()); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return nil, model.NewError(model.CodeVerify,
			"cannot create output dir: "+err.Error()).WithFile(staged)
	}
	if err := os.WriteFile(staged, w.buf.Bytes(), 0o644); err != nil {
		return nil, model.NewError(model.CodeVerify,
			"cannot stage output: "+err.Error()).WithFile(staged)
	}
	mf.SampleCount = len(mf.Records)
	return mf, nil
}

func writeStr(w io.Writer, s string) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(len(s)))
	w.Write(b[:])
	io.WriteString(w, s)
}

func boxBytesLocal(typ string, payload []byte) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	return append(hdr, payload...)
}
