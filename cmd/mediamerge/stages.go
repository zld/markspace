package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"mediamerge/internal/container"
	"mediamerge/internal/manifest"
	"mediamerge/internal/transport"
)

const manifestTimescale = 90000

func sha256File(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func digestOf(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// readRTPStream decodes the length-prefixed datagram file and runs it
// through the jitter buffer.
func readRTPStream(path string, window uint64) ([]transport.StreamPacket, transport.Stats, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, transport.Stats{}, err
	}
	jb, err := transport.NewJitterBuffer(window)
	if err != nil {
		return nil, transport.Stats{}, err
	}
	u := &transport.Unwrapper{}
	for off := 0; off+4 <= len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[off:]))
		off += 4
		if n <= 0 || off+n > len(raw) {
			return nil, transport.Stats{}, fmt.Errorf("transport: bad datagram at %d", off)
		}
		p, err := transport.ParseRTP(raw[off : off+n])
		if err != nil {
			return nil, transport.Stats{}, err
		}
		jb.Push(transport.Normalize(p, u))
		off += n
	}
	var out []transport.StreamPacket
	for {
		p, ok := jb.Next()
		if !ok {
			break
		}
		out = append(out, p)
	}
	out = append(out, jb.Flush()...)
	return out, jb.Stats(), nil
}

// mergeSegments splits committed media into 0.5 s segments and computes the
// actual bitrate from sample bytes over exact rational durations.
func mergeSegments(cfg config, fi *container.FileInfo, tables map[uint32]*container.SampleTable) ([]manifest.Segment, int64, error) {
	segTicks := int64(manifestTimescale / 2) // 0.5 s
	var maxEnd int64
	type bs struct{ start, end, bytes int64 }
	var spans []bs
	var totalBytes int64
	for _, st := range tables {
		for _, smp := range st.Samples {
			t, err := smp.Timestamp.Ticks(manifestTimescale)
			if err != nil {
				return nil, 0, err
			}
			seg := t / segTicks
			for int64(len(spans)) <= seg {
				spans = append(spans, bs{start: int64(len(spans)) * segTicks, end: int64(len(spans))*segTicks + segTicks})
			}
			spans[seg].bytes += int64(smp.Size)
			totalBytes += int64(smp.Size)
		}
		end, err := st.Duration.Ticks(manifestTimescale)
		if err != nil {
			return nil, 0, err
		}
		if end > maxEnd {
			maxEnd = end
		}
	}
	var segs []manifest.Segment
	for i, sp := range spans {
		segs = append(segs, manifest.Segment{
			Index: i, Start: sp.start, End: sp.end,
			TargetBps:  cfg.targetBps,
			ActualBps:  sp.bytes * 8 * manifestTimescale / segTicks,
			GapPackets: 0, CompleteSamples: 1, TotalSamples: 1,
		})
	}
	if maxEnd == 0 {
		return nil, 0, fmt.Errorf("media: zero media duration")
	}
	actual := totalBytes * 8 * manifestTimescale / maxEnd
	return segs, actual, nil
}

// corruptFile flips one byte inside path (post-commit corruption fault).
func corruptFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) < 32 {
		return fmt.Errorf("file too small to corrupt")
	}
	raw[16] ^= 0xFF
	return os.WriteFile(path, raw, 0o644)
}
