package engine

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fmt"

	"mediarun/internal/pipeline"
)

// assemble concatenates committed normalized WAV data chunks under one header.
func (r *Runner) assemble(cfg Config, records []pipeline.SegmentRecord) (string, string, error) {
	var data []byte
	sorted := append([]pipeline.SegmentRecord(nil), records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })
	for _, rec := range sorted {
		p, _ := pipeline.SafeJoin(cfg.WorkDir, "segments", rec.OutputFile)
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", "", err
		}
		if len(raw) <= 44 {
			return "", "", errors.New("normalized segment missing PCM data")
		}
		data = append(data, raw[44:]...)
	}
	out := make([]byte, 44+len(data))
	writeWAVHeader(out, uint32(len(data)), uint32(r.params.SampleRate), uint16(r.params.Channels))
	copy(out[44:], data)
	finalPath, _ := pipeline.SafeJoin(cfg.WorkDir, "output.wav")
	if err := os.WriteFile(finalPath, out, 0o644); err != nil {
		return "", "", err
	}
	return finalPath, pipeline.DigestSHA256(out), nil
}

func segmentArtifact(cfg Config, index int) string {
	p, _ := pipeline.SafeJoin(cfg.WorkDir, "segments", segmentInName(index))
	return p
}

func segmentInName(index int) string {
	return "seg" + itoa(index) + ".wav"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ensureSafePath rejects path traversal. The raw path is inspected before any
// cleaning (which would resolve ".." away): any parent-reference component is
// refused. Sibling directories given as clean absolute paths remain allowed.
func ensureSafePath(target string) error {
	if target == "" {
		return fmt.Errorf("%w: empty path", pipeline.ErrPathEscape)
	}
	for _, part := range strings.Split(filepath.ToSlash(target), "/") {
		if part == ".." {
			return fmt.Errorf("%w: traversal segment in %q", pipeline.ErrPathEscape, target)
		}
	}
	if _, err := filepath.Abs(filepath.Clean(target)); err != nil {
		return fmt.Errorf("%w: %v", pipeline.ErrPathEscape, err)
	}
	return nil
}

// writeWAVHeader fills a 44-byte PCM s16 WAV header.
func writeWAVHeader(buf []byte, dataSize, rate uint32, channels uint16) {
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], 36+dataSize)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], channels)
	binary.LittleEndian.PutUint32(buf[24:28], rate)
	binary.LittleEndian.PutUint32(buf[28:32], rate*uint32(channels)*2)
	binary.LittleEndian.PutUint16(buf[32:34], channels*2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataSize)
}
