package fixture

import (
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/index"
)

// MaxInputSize enforces the 2 GiB per-input operating limit.
const MaxInputSize int64 = 2 << 30

// Representation names used across generated manifests.
const (
	RepVideoHi = "v-hi"
	RepVideoLo = "v-lo"
	RepAudio   = "a-main"
)

// Sample describes one generated media frame for the pipeline.
type Sample struct {
	Track          string
	Representation string
	SegmentNumber  int
	IsKeyFrame     bool
	RawTS          uint32
	Timescale      uint32
	TrueNS         int64 // ground-truth programme instant (from zero)
	ObservedNS     int64 // drifted observed programme instant
	ByteOffset     int64
	InputSize      int64
	Truncated      bool
	Discontinuity  bool
	Wrapped        bool // raw 32-bit timestamp wrapped relative to prior
}

// PacketSpec describes one generated RTP packet.
type PacketSpec struct {
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	Payload        []byte
	Missing        bool // simulated backhaul drop
	Retransmitted  bool // dropped once, then re-sent inside the window
	SegmentNumber  int
}

// Bundle is everything generated for one scenario.
type Bundle struct {
	Root          string
	Samples       []Sample
	Packets       []PacketSpec
	MPD           []byte
	HLS           []byte
	InputSizes    map[string]int64
	SegmentFiles  map[string]int // relative path -> declared length
	TruncatedSegs []string
	DriftPPM      float64
	Healthy       bool
	Anchors       []AnchorSpec
}

// AnchorSpec is a ground-truth drift anchor emitted for repair fitting.
type AnchorSpec struct {
	Track  string
	ObsNS  int64
	TrueNS int64
}

// Options controls deterministic sample generation.
type Options struct {
	Seed         int64
	Segments     int // media segments per representation
	FramesPerSeg int
	ChunkRecords int
	Root         string
	// Healthy disables all fault injection (drift, wrap, VFR shifts,
	// RTP loss and truncation) for the clean baseline summary.
	Healthy bool
}

// videoTimescale is the 90kHz track clock; audio runs at 48kHz.
const (
	videoTS uint32 = 90000
	audioTS uint32 = 48000
)

// Generate writes the full healthy+damaged fixture tree and returns the
// in-memory descriptors the pipeline consumes. workspaceRoot bounds every
// generated path; root must stay inside it.
func Generate(workspaceRoot string, opts Options) (*Bundle, error) {
	if err := index.EnsureWithin(opts.Root, workspaceRoot); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Root, 0o755); err != nil {
		return nil, err
	}
	r := newRNG(opts.Seed)
	b := &Bundle{
		Root:         opts.Root,
		InputSizes:   map[string]int64{},
		SegmentFiles: map[string]int{},
		DriftPPM:     120, // 120ppm slow clock, deterministic
		Healthy:      opts.Healthy,
	}
	if opts.Healthy {
		b.DriftPPM = 0
	}
	if err := b.writeMedia(r, opts); err != nil {
		return nil, err
	}
	b.writeRTP(r, opts)
	b.MPD = b.buildMPD(opts)
	b.HLS = b.buildHLS(opts)
	if err := os.WriteFile(filepath.Join(opts.Root, "manifest.mpd"), b.MPD, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(opts.Root, "playlist.m3u8"), b.HLS, 0o644); err != nil {
		return nil, err
	}
	return b, nil
}

// writeMedia synthesises init + media segments for all three tracks and
// injects drift, wrap, VFR shifts and one truncated segment.
func (b *Bundle) writeMedia(r *rng, opts Options) error {
	tracks := []struct {
		name, rep string
		ts        uint32
	}{
		{"video", RepVideoHi, videoTS},
		{"video", RepVideoLo, videoTS},
		{"audio", RepAudio, audioTS},
	}
	// VFR frame durations (ticks) cycling through four intervals, so the
	// fixture always exhibits >=3 distinct interval shifts.
	vfrDurs := []uint32{2700, 3003, 3300, 3003}
	for _, tk := range tracks {
		dir := filepath.Join(opts.Root, tk.rep)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		init := InitSegment(tk.ts, 1)
		initPath := filepath.Join(dir, "init.mp4")
		if err := os.WriteFile(initPath, init, 0o644); err != nil {
			return err
		}
		var rawCounter uint32 // 32-bit video timestamp that will wrap
		var totalSize int64
		var prevRaw uint32
		havePrev := false
		var trueTicks int64 // ground-truth tick cursor on this timescale
		// Start the video counters near the 32-bit ceiling so at least one
		// real wraparound occurs. This is a local encoding effect only:
		// TrueNS stays anchored at zero, and repair's unwrap expansion
		// recovers the monotonic programme instant. Healthy runs start at
		// zero and never wrap.
		if tk.name == "video" && !opts.Healthy {
			rawCounter = 0xFFFFFFFF - uint32(opts.FramesPerSeg*3)*3300
		}
		for seg := 1; seg <= opts.Segments; seg++ {
			n := opts.FramesPerSeg
			durs := make([]uint32, n)
			for i := range durs {
				if tk.name == "video" {
					if opts.Healthy {
						durs[i] = 3003 // uniform 29.97fps cadence when healthy
					} else {
						durs[i] = vfrDurs[(seg+i)%len(vfrDurs)]
					}
				} else {
					// Match the corresponding video frame's nanosecond
					// spacing, expressed in 48kHz audio ticks.
					vidDur := uint32(3003)
					if !opts.Healthy {
						vidDur = vfrDurs[(seg+i)%len(vfrDurs)]
					}
					durs[i] = uint32(int64(vidDur) * int64(tk.ts) / int64(videoTS))
				}
			}
			baseTS := rawCounter
			trunc := !opts.Healthy && seg == opts.Segments &&
				tk.rep == RepVideoHi
			data := MediaSegment(uint32(seg), 1, baseTS, durs, 64, trunc)
			if int64(len(data)) > MaxInputSize {
				return fmt.Errorf("segment exceeds 2GiB limit")
			}
			rel := filepath.Join(tk.rep, fmt.Sprintf("seg-%d.m4s", seg))
			path := filepath.Join(opts.Root, rel)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			b.SegmentFiles[rel] = len(data)
			totalSize += int64(len(data))
			if trunc {
				b.TruncatedSegs = append(b.TruncatedSegs, rel)
			}
			// One Sample per frame. ObservedNS carries drift only; the
			// 32-bit wrap is tracked separately on the raw counter.
			var tsCursor uint32 = baseTS
			for i := 0; i < n; i++ {
				trueNS := trueTicks * 1_000_000_000 / int64(tk.ts)
				obsNS := b.driftedNS(trueNS)
				wrapped := havePrev && tsCursor < prevRaw
				s := Sample{
					Track:          tk.name,
					Representation: tk.rep,
					SegmentNumber:  seg,
					IsKeyFrame:     tk.name == "video" && i%2 == 0,
					RawTS:          tsCursor,
					Timescale:      tk.ts,
					TrueNS:         trueNS,
					ObservedNS:     obsNS,
					ByteOffset:     int64(i * 80),
					InputSize:      totalSize,
					Truncated:      trunc && i == n-1,
					Wrapped:        wrapped,
				}
				b.Samples = append(b.Samples, s)
				prevRaw, havePrev = tsCursor, true
				tsCursor += durs[i]
				rawCounter += durs[i]
				trueTicks += int64(durs[i])
			}
		}
		b.InputSizes[tk.rep] = totalSize
	}
	return b.emitAnchors()
}

// driftedNS applies the deterministic slow-clock drift to a true instant.
func (b *Bundle) driftedNS(trueNS int64) int64 {
	return int64(float64(trueNS) / (1 + b.DriftPPM/1e6))
}

// emitAnchors records periodic ground-truth anchors for drift fitting:
// one anchor per track for every even segment.
func (b *Bundle) emitAnchors() error {
	seen := map[string]bool{}
	for _, s := range b.Samples {
		if s.SegmentNumber%2 != 0 {
			continue
		}
		key := s.Track + "|" + itoa(s.SegmentNumber)
		if seen[key] {
			continue
		}
		seen[key] = true
		b.Anchors = append(b.Anchors, AnchorSpec{
			Track:  s.Track,
			ObsNS:  s.ObservedNS,
			TrueNS: s.TrueNS,
		})
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
