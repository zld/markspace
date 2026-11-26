// Package fingerprint computes stable SHA-256 hashes for output files,
// decoded video frames and canonical audio windows.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
)

// Locator identifies a frame or audio window inside a generated segment.
type Locator struct {
	Segment int    `json:"segment"`
	Kind    string `json:"kind"`
	Index   int64  `json:"index"`
	PTSNS   int64  `json:"pts_ns"`
}

// Group links identical content without modifying or removing any copy.
type Group struct {
	SHA256    string    `json:"sha256"`
	Locations []Locator `json:"locations"`
}

// Bytes returns the hex SHA-256 of b.
func Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// File streams a file and returns its hex SHA-256 and byte size.
func File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Frame hashes canonical metadata followed by exactly the decoded pixel
// bytes. Metadata prevents identical pixel buffers at different dimensions
// or PTS from being treated as the same timed frame accidentally; duplicate
// detection uses PixelHash separately when comparing visual content.
func Frame(ptsNS int64, width, height int, pixFmt string, pixels []byte) string {
	h := sha256.New()
	writeCanonicalHeader(h, "frame", 1)
	var meta [8]byte
	binary.LittleEndian.PutUint64(meta[:], uint64(ptsNS))
	h.Write(meta[:])
	binary.LittleEndian.PutUint32(meta[:4], uint32(width))
	h.Write(meta[:4])
	binary.LittleEndian.PutUint32(meta[:4], uint32(height))
	h.Write(meta[:4])
	io.WriteString(h, pixFmt)
	h.Write([]byte{0})
	h.Write(pixels)
	return hex.EncodeToString(h.Sum(nil))
}

// Pixel returns a hash of only canonical pixel bytes and dimensions. It is
// used for cross-segment duplicate visual content.
func Pixel(width, height int, pixels []byte) string {
	h := sha256.New()
	writeCanonicalHeader(h, "pixel", 1)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(width))
	h.Write(b[:])
	binary.LittleEndian.PutUint32(b[:], uint32(height))
	h.Write(b[:])
	h.Write(pixels)
	return hex.EncodeToString(h.Sum(nil))
}

// AudioWindow hashes mono s16le PCM together with sample rate and interval.
func AudioWindow(startNS, endNS int64, sampleRate int, pcmS16LE []byte) string {
	h := sha256.New()
	writeCanonicalHeader(h, "audio-window", 1)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(startNS))
	h.Write(b[:])
	binary.LittleEndian.PutUint64(b[:], uint64(endNS))
	h.Write(b[:])
	binary.LittleEndian.PutUint32(b[:4], uint32(sampleRate))
	h.Write(b[:4])
	h.Write(pcmS16LE)
	return hex.EncodeToString(h.Sum(nil))
}

func writeCanonicalHeader(h hash.Hash, kind string, version uint16) {
	var v [2]byte
	binary.LittleEndian.PutUint16(v[:], version)
	io.WriteString(h, "pwflow-fp/")
	io.WriteString(h, kind)
	h.Write([]byte{'/', v[0], v[1], '\n'})
}

// Registry records content hashes and produces duplicate groups. Every
// occurrence remains in the manifest; groups are annotations only.
type Registry struct {
	locations map[string][]Locator
}

// NewRegistry constructs an empty duplicate registry.
func NewRegistry() *Registry {
	return &Registry{locations: make(map[string][]Locator)}
}

// Add records one occurrence. It reports whether this is a duplicate of
// previously seen content.
func (r *Registry) Add(sha string, loc Locator) bool {
	old := r.locations[sha]
	r.locations[sha] = append(old, loc)
	return len(old) > 0
}

// Groups returns hashes occurring more than once in deterministic order.
func (r *Registry) Groups() []Group {
	var out []Group
	for sha, locs := range r.locations {
		if len(locs) > 1 {
			locs = append([]Locator(nil), locs...)
			sort.Slice(locs, func(i, j int) bool {
				if locs[i].Segment != locs[j].Segment {
					return locs[i].Segment < locs[j].Segment
				}
				if locs[i].Kind != locs[j].Kind {
					return locs[i].Kind < locs[j].Kind
				}
				return locs[i].Index < locs[j].Index
			})
			out = append(out, Group{SHA256: sha, Locations: locs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SHA256 < out[j].SHA256 })
	return out
}

// VerifyFile compares a streamed file hash with want.
func VerifyFile(path, want string) error {
	got, _, err := File(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("hash mismatch for %s: got %s want %s", path, got, want)
	}
	return nil
}
