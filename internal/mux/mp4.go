// Package mux performs lossless remuxing into MP4. Compressed sample bytes are
// copied verbatim; this package never decodes or re-encodes media.
package mux

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/demux"
	"medpipe/internal/fault"
)

// Result summarizes a readable MP4 output.
type Result struct {
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"output_sha256"`
	TrackCount  int    `json:"track_count"`
	SampleCount int    `json:"sample_count"`
}

// WriteMP4 writes mapped tracks to path using go-mp4's box writer. The file is
// created directly at path; transactional callers should therefore pass a
// temporary path in the destination directory and rename after verification.
func WriteMP4(path string, mappings []demux.Mapping) (*Result, *fault.Error) {
	if len(mappings) == 0 {
		return nil, fault.New(fault.ErrUnmappableTrack, "mux.mp4", 0, "no tracks mapped")
	}
	for _, m := range mappings {
		if m.Track == nil {
			return nil, fault.New(fault.ErrUnmappableTrack, "mux.mp4", 0, "nil mapped track")
		}
		switch m.Track.Codec {
		case "avc1", "hvc1", "mp4a":
		default:
			return nil, fault.New(fault.ErrUnknownCodec, "mux.mp4", 0,
				"refusing to write unsupported codec "+m.Track.Codec).WithPID(m.Track.PID)
		}
		var last int64
		for i, s := range m.Track.Samples {
			if len(s.Data) == 0 {
				return nil, fault.New(fault.ErrCorruptSample, "mux.mp4", s.Offset, "refusing to write empty sample")
			}
			if i > 0 && s.PTSMS < last {
				return nil, fault.New(fault.ErrMonotonic, "mux.mp4", s.Offset, "mapped sample timestamps are not monotonic")
			}
			last = s.PTSMS
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "mux.mp4.create", 0, path, err)
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()

	w := gomp4.NewWriter(f)
	if fe := writeFileType(w); fe != nil {
		return nil, fe
	}

	offsets := make([][]uint32, len(mappings))
	bi, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdat()})
	if err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "mux.mp4.mdat", 0, "start mdat", err)
	}
	_ = bi
	sampleCount := 0
	for mi, mapping := range mappings {
		for _, s := range mapping.Track.Samples {
			off, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, fault.Wrap(fault.ErrCommit, "mux.mp4.offset", 0, "seek mdat offset", err)
			}
			offsets[mi] = append(offsets[mi], uint32(off))
			if _, err := w.Write(s.Data); err != nil {
				return nil, fault.Wrap(fault.ErrCommit, "mux.mp4.write", off, "write sample", err)
			}
			sampleCount++
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "mux.mp4.mdat", 0, "end mdat", err)
	}

	if fe := writeMovie(w, mappings, offsets); fe != nil {
		return nil, fe
	}
	if err := f.Close(); err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "mux.mp4.close", 0, "close MP4", err)
	}
	if fe := VerifyReadable(path); fe != nil {
		return nil, fe
	}
	sum, size, fe := hashFile(path)
	if fe != nil {
		return nil, fe
	}
	ok = true
	return &Result{Path: path, Bytes: size, SHA256: sum, TrackCount: len(mappings), SampleCount: sampleCount}, nil
}

func hashFile(path string) (string, int64, *fault.Error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fault.Wrap(fault.ErrOutputUnreadable, "mux.verify", 0, "reopen output", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, tsMaxOutput+1))
	if err != nil {
		return "", 0, fault.Wrap(fault.ErrOutputUnreadable, "mux.verify", 0, "hash output", err)
	}
	if n > tsMaxOutput {
		return "", n, fault.New(fault.ErrInputTooLarge, "mux.verify", 0, "output exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

const tsMaxOutput = 128 << 20
