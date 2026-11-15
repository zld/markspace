// Package synth (imported as fixture) generates deterministic CMAF init
// segments and moof/mdat fragments on disk, including optional fault
// injection: reordered delivery, a missing fragment and timeline drift.
//
// Everything is derived from the numeric seed so repeated runs with the same
// arguments produce byte-identical output.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// Timescale is 1000 so one timescale unit equals one millisecond and the
	// rest of the pipeline can reason in pure integer milliseconds.
	Timescale uint32 = 1000
	// SamplesPerFragment splits each 500ms part into four 125ms samples; the
	// first sample of every fragment is a sync point (see sample flags).
	SamplesPerFragment = 4
	sampleDuration     = 125
	samplePayloadSize  = 96
	// DriftMs is the fixed cross-bitrate offset injected by the "drift" fault.
	DriftMs = 80
	initName = "init.mp4"
	idxName  = "delivery.idx"
)

// TrackMeta describes one generated bitrate directory.
type TrackMeta struct {
	Bitrate    int
	Timescale  uint32
	Present    []int // fragment sequence numbers actually written, sorted
	Missing    []int // sequence numbers intentionally dropped
	DriftMs    int64 // tfdt offset applied to every fragment
	Dir        string
	Delivered  []string // delivery.idx lines in ingest order
}

// Options controls synthesis.
type Options struct {
	Seed       int64
	PartMs     int64
	DurationMs int64
	Faults     []string
}

func hasFault(faults []string, name string) bool {
	for _, f := range faults {
		if f == name {
			return true
		}
	}
	return false
}

// Generate writes one directory per bitrate under root and returns its meta.
func Generate(root string, bitrates []int, opts Options) ([]TrackMeta, error) {
	if opts.PartMs <= 0 || opts.DurationMs <= 0 || opts.DurationMs%opts.PartMs != 0 {
		return nil, fmt.Errorf("fixture: duration must be a multiple of part")
	}
	count := int(opts.DurationMs / opts.PartMs)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	metas := make([]TrackMeta, 0, len(bitrates))
	for ti, br := range bitrates {
		meta := TrackMeta{Bitrate: br, Timescale: Timescale,
			Dir: filepath.Join(root, strconv.Itoa(br))}
		if err := os.MkdirAll(meta.Dir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(meta.Dir, initName),
			buildInit(), 0o644); err != nil {
			return nil, err
		}
		// Drift hits the first bitrate, a missing fragment the second.
		if hasFault(opts.Faults, "drift") && ti == 0 {
			meta.DriftMs = DriftMs
		}
		missingSeq := -1
		if hasFault(opts.Faults, "missing") && ti == 1 {
			missingSeq = count/2 + 1
			meta.Missing = []int{missingSeq}
		}
		for seq := 1; seq <= count; seq++ {
			if seq == missingSeq {
				continue
			}
			baseTime := uint64((seq-1)*int(opts.PartMs)) + uint64(meta.DriftMs)
			data := BuildFragment(uint32(seq), baseTime, br)
			name := fragmentName(seq)
			if err := os.WriteFile(filepath.Join(meta.Dir, name), data, 0o644); err != nil {
				return nil, err
			}
			meta.Present = append(meta.Present, seq)
		}
		meta.Delivered = buildDeliveryOrder(meta.Present, opts.Seed+int64(br),
			hasFault(opts.Faults, "order"))
		if err := os.WriteFile(filepath.Join(meta.Dir, idxName),
			[]byte(strings.Join(meta.Delivered, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
		metas = append(metas, meta)
	}
	return metas, nil
}

// FragmentName is exported so the collector can reuse the naming convention.
func FragmentName(seq int) string { return fragmentName(seq) }

func fragmentName(seq int) string {
	return fmt.Sprintf("frag-%05d.mp4", seq)
}

// ParseFragmentName returns the sequence number encoded in a fragment file.
// Names follow frag-NNNNN with an optional trailing tag before the extension
// (for example frag-00001b.mp4); the sequence is always authoritative from
// the fragment's mfhd box, this helper only recognizes the on-disk family.
func ParseFragmentName(name string) (int, bool) {
	base := filepath.Base(name)
	if !strings.HasPrefix(base, "frag-") || !strings.HasSuffix(base, ".mp4") {
		return 0, false
	}
	digits := base[5:]
	digits = digits[:len(digits)-4] // strip ".mp4"
	numEnd := 0
	for numEnd < len(digits) && digits[numEnd] >= '0' && digits[numEnd] <= '9' {
		numEnd++
	}
	if numEnd == 0 {
		return 0, false
	}
	v, err := strconv.Atoi(digits[:numEnd])
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// buildDeliveryOrder returns filenames in ingest order. The order fault
// shuffles deterministically and repeats one entry to exercise the
// idempotent duplicate path.
func buildDeliveryOrder(seqs []int, seed int64, reorder bool) []string {
	names := make([]string, len(seqs))
	for i, seq := range seqs {
		names[i] = fragmentName(seq)
	}
	if !reorder || len(names) < 2 {
		return names
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	dup := names[0]
	out := make([]string, 0, len(names)+1)
	out = append(out, names...)
	insertAt := 1 + int(seed%int64(len(names)))
	out = append(out[:insertAt], append([]string{dup}, out[insertAt:]...)...)
	return out
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], []byte(typ))
	copy(out[8:], payload)
	return out
}

func fullBox(typ string, version byte, flags uint32, body []byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(hdr, body...))
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func buildInit() []byte {
	mdhd := fullBox("mdhd", 0, 0, append(make([]byte, 8), u32(Timescale)...))
	mdia := box("mdia", mdhd)
	trak := box("trak", mdia)
	moov := box("moov", trak)
	return append(box("ftyp", []byte("dash")), moov...)
}

// BuildFragment emits a moof+mdat fragment. Payload bytes are a deterministic
// function of bitrate and sequence so re-delivery hashes identically while
// different sequences never collide. It is exported for tests in other
// packages that need valid fragments with bespoke sequence numbers.
func BuildFragment(seq uint32, baseTime uint64, bitrate int) []byte {
	mfhd := fullBox("mfhd", 0, 0, u32(seq))
	tfhd := fullBox("tfhd", 0, 0x020000, u32(1)) // default-base-is-moof
	tfdt := fullBox("tfdt", 0, 0, u32(uint32(baseTime)))
	var body []byte
	body = append(body, u32(SamplesPerFragment)...)
	body = append(body, u32(0)...) // data_offset placeholder
	for i := 0; i < SamplesPerFragment; i++ {
		body = append(body, u32(sampleDuration)...)
		body = append(body, u32(samplePayloadSize)...)
	}
	trun := fullBox("trun", 0, 0x000001|0x000100|0x000200, body)
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	moof := box("moof", append(mfhd, traf...))
	mdat := box("mdat", deterministicPayload(bitrate, int(seq)))
	return append(moof, mdat...)
}

func deterministicPayload(bitrate, seq int) []byte {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(bitrate))
	binary.BigEndian.PutUint32(buf[4:], uint32(seq))
	h.Write(buf[:])
	seed := h.Sum(nil)
	payload := make([]byte, samplePayloadSize*SamplesPerFragment)
	for i := range payload {
		payload[i] = seed[i%len(seed)] ^ byte(i)
	}
	return payload
}

// SortedBitrateDirs lists numeric bitrate directories under root.
func SortedBitrateDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dirs = append(dirs, e.Name())
	}
	sort.Strings(dirs)
	return dirs, nil
}

// InitName and IndexName expose the on-disk file conventions.
func InitName() string  { return initName }
func IndexName() string { return idxName }
