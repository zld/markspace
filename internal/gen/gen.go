// Package gen synthesizes the deterministic fixtures the acceptance run uses.
//
// Everything is produced from a fixed seed: no external files are read and no
// timestamps come from the wall clock, so repeated generations are byte
// identical. Each fault class is injected by post-processing a good stream at a
// known byte offset, which lets the tests assert exact error locations.
package gen

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediarun/internal/fault"
	"mediarun/internal/frames"
)

// Options controls fixture synthesis.
type Options struct {
	Dir           string
	SegmentCount  int
	FramesPerSeg  int
	KeyframeEvery int
	// AudioOffsetNs shifts the audio track start relative to video.
	AudioOffsetNs int64
	// AudioStartLateNs additionally delays the first audio packet.
	AudioStartLateNs int64
	// Inject lists fault classes: truncated, checksum, seqwrap, overflow,
	// desync, gap, dupe.
	Inject []string
	// NoiseBytes is the size of the resynchronisation filler inserted after
	// truncated records.
	NoiseBytes int
}

// Result describes what was generated.
type Result struct {
	FramesPath string
	AudioPath  string
	StatsPath  string
	Frames     int
	Packets    int
	BaseRTP    uint32
	Injected   []string
	// Faults records what the generator deliberately damaged.
	InjectedOffsets []int64
}

// Defaults fills zero fields.
func (o *Options) Defaults() {
	if o.SegmentCount == 0 {
		o.SegmentCount = 4
	}
	if o.FramesPerSeg == 0 {
		o.FramesPerSeg = 8
	}
	if o.KeyframeEvery == 0 {
		o.KeyframeEvery = 10
	}
	if o.NoiseBytes == 0 {
		o.NoiseBytes = 48
	}
}

// KnownInjections are the accepted fault class names.
var KnownInjections = []string{"truncated", "checksum", "seqwrap", "overflow", "desync", "gap", "dupe"}

// ValidateInjections rejects unknown fault classes early.
func ValidateInjections(list []string) error {
	known := map[string]bool{}
	for _, k := range KnownInjections {
		known[k] = true
	}
	for _, k := range list {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !known[k] {
			return fmt.Errorf("unknown injection %q (known: %s)", k, strings.Join(KnownInjections, ","))
		}
	}
	return nil
}

// Generate writes frames.mfr, audio.rtp and audio.stats under opts.Dir and
// returns their paths. The run directory doubles as the pipeline input
// directory, so audio.stats is found automatically.
func Generate(opts Options) (*Result, error) {
	opts.Defaults()
	if err := ValidateInjections(opts.Inject); err != nil {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "%v", err)
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "create %s", opts.Dir)
	}
	inject := map[string]bool{}
	for _, k := range opts.Inject {
		inject[strings.TrimSpace(k)] = true
	}

	total := opts.SegmentCount * opts.FramesPerSeg
	frameBuf, recs, err := buildFrames(opts, total, inject)
	if err != nil {
		return nil, err
	}
	framesPath := filepath.Join(opts.Dir, "frames.mfr")
	if err := os.WriteFile(framesPath, frameBuf, 0o644); err != nil {
		return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "write %s", framesPath)
	}

	audio, stats, err := buildAudio(opts, total, inject)
	if err != nil {
		return nil, err
	}
	audioPath := filepath.Join(opts.Dir, "audio.rtp")
	if err := os.WriteFile(audioPath, audio, 0o644); err != nil {
		return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "write %s", audioPath)
	}
	statsPath := filepath.Join(opts.Dir, "audio.stats")
	if err := writeStats(statsPath, stats); err != nil {
		return nil, err
	}
	return &Result{
		FramesPath:      framesPath,
		AudioPath:       audioPath,
		StatsPath:       statsPath,
		Frames:          len(recs),
		Packets:         stats.Packets,
		BaseRTP:         stats.BaseRTP,
		Injected:        sortedKeys(inject),
		InjectedOffsets: recs,
	}, nil
}

// buildFrames encodes a valid stream, then applies the record-level
// injections. It returns the damaged bytes and the absolute offsets the
// injections landed on.
func buildFrames(opts Options, total int, inject map[string]bool) ([]byte, []int64, error) {
	var buf bytes.Buffer
	var offsets []int64
	step := int64(90000) * int64(1) / 25 // 25 fps at 90 kHz
	// Keyframes land on the requested cadence and on every nominal segment
	// boundary, mirroring how a real encoder is forced to close a fragment.
	segStart := func(i int) bool { return i%opts.FramesPerSeg == 0 }
	_ = segStart
	for i := 0; i < total; i++ {
		payload := makePayload(i)
		flags := uint16(0)
		if i%opts.KeyframeEvery == 0 || i%opts.FramesPerSeg == 0 {
			flags |= frames.FlagKeyframe
		}
		if i == total-1 {
			flags |= frames.FlagEndOfSeq
		}
		pts := uint64(i) * uint64(step)
		offsets = append(offsets, int64(buf.Len()))
		if _, err := frames.Encode(&buf, frames.EncodeOptions{
			Flags: flags,
			Track: 1,
			PTS:   pts,
			DTS:   pts,
		}, payload); err != nil {
			return nil, nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "encode frame %d", i)
		}
	}
	data := buf.Bytes()

	if inject["checksum"] {
		// Flip one payload byte in the third record, leaving framing intact so
		// the scanner can still advance to the next record.
		if len(offsets) > 2 {
			off := int(offsets[2]) + frames.HeaderSize + 6
			if off < len(data) {
				data[off] ^= 0xFF
			}
		}
	}
	if inject["truncated"] {
		// Cut the stream inside the final record. The declared length now runs
		// past EOF, so the scanner reports a truncation at that record's own
		// offset instead of guessing; earlier records are untouched.
		if last := len(offsets) - 1; last > 0 {
			start := int(offsets[last])
			span := len(data) - start
			if span > frames.HeaderSize+frames.DigestSize+4 {
				data = data[:start+frames.HeaderSize+4]
			}
		}
	}
	return data, offsets, nil
}

// makePayload builds a recognisable, deterministic image payload.
func makePayload(i int) []byte {
	const size = 64
	p := make([]byte, size)
	copy(p, "IMG1")
	binary.BigEndian.PutUint32(p[4:8], uint32(i))
	binary.BigEndian.PutUint32(p[8:12], 1280)
	binary.BigEndian.PutUint32(p[12:16], 720)
	seed := sha256.Sum256([]byte(fmt.Sprintf("frame-%d", i)))
	copy(p[16:], seed[:])
	for j := 48; j < size; j++ {
		p[j] = byte(i*7 + j)
	}
	return p
}
