package mediakernel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pion/rtp"
)

// PipelineConfig bounds resources and configures detection stages.
type PipelineConfig struct {
	QueueCap            int   // bounded queue capacity (clusters in flight)
	MaxBlockSize        int64 // per-block size limit
	SegmentDurNs        int64 // segment duration (half-open intervals)
	Silence             SilenceConfig
	FingerprintWindowNs int64
	FailAfterSegments   int // test hook: abort after committing N segments
}

func (c *PipelineConfig) defaults() {
	if c.QueueCap <= 0 {
		c.QueueCap = 4
	}
	if c.MaxBlockSize <= 0 {
		c.MaxBlockSize = DefaultMaxBlock
	}
	if c.SegmentDurNs <= 0 {
		c.SegmentDurNs = 1_000_000_000
	}
	if c.FingerprintWindowNs <= 0 {
		c.FingerprintWindowNs = 1_000_000_000
	}
}

// Pipeline runs parse -> segment -> silence -> fingerprint with checkpointing.
type Pipeline struct {
	cfg    PipelineConfig
	outDir string
}

func NewPipeline(cfg PipelineConfig, outDir string) *Pipeline {
	cfg.defaults()
	return &Pipeline{cfg: cfg, outDir: outDir}
}

// Params records the effective parameters in the manifest.
func (p *Pipeline) Params() map[string]int64 {
	return map[string]int64{
		"queueCap":            int64(p.cfg.QueueCap),
		"maxBlockSize":        p.cfg.MaxBlockSize,
		"segmentDurNs":        p.cfg.SegmentDurNs,
		"silenceThreshold":    int64(p.cfg.Silence.Threshold),
		"silenceMinDurNs":     p.cfg.Silence.MinDuration,
		"silenceMaxGapNs":     p.cfg.Silence.MaxGap,
		"fingerprintWindowNs": p.cfg.FingerprintWindowNs,
	}
}

// Run executes the pipeline over inputPath. With resume, a checkpoint from a
// previous interrupted run is reused; the input digest must match.
func (p *Pipeline) Run(ctx context.Context, inputPath string, resume bool) (*Manifest, error) {
	inputSHA, size, err := sha256File(inputPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.outDir, 0o755); err != nil {
		return nil, err
	}
	ckptPath := filepath.Join(p.outDir, "checkpoint.json")
	var ckpt *Checkpoint
	if resume {
		ckpt, err = LoadCheckpoint(ckptPath)
		if err != nil {
			return nil, err
		}
		if ckpt != nil {
			if err := ckpt.Validate(inputSHA); err != nil {
				return nil, err
			}
		}
	}
	if ckpt == nil {
		ckpt = &Checkpoint{InputSHA256: inputSHA, Stage: "start"}
	}
	skipBefore := ckpt.ProcessedBlocks

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type clusterMsg struct {
		m      *Media
		blocks []Block
	}
	q := make(chan clusterMsg, p.cfg.QueueCap)
	prodErr := make(chan error, 1)
	go func() {
		f, err := os.Open(inputPath)
		if err != nil {
			prodErr <- err
			close(q)
			return
		}
		defer f.Close()
		_, err = ParseStream(ctx, f, size, p.cfg.MaxBlockSize,
			func(m *Media, blocks []Block) error {
				select {
				case q <- clusterMsg{m, blocks}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		prodErr <- err
		close(q)
	}()

	var clock *Clock
	var media *Media
	seg := NewSegmenter(p.cfg.SegmentDurNs)
	silence := NewSilenceDetector(p.cfg.Silence)
	fp := NewFingerprinter(p.cfg.FingerprintWindowNs)
	if skipBefore > 0 {
		silence.Restore(ckpt.Silence)
		fp.Restore(ckpt.Fingerprints)
	}
	processed := int64(0)
	maxOff := ckpt.CommittedOffset
	cur := ckpt.Partial

	// consume processes one cluster: stamp, sort stably by (ns, track,
	// offset), feed the stages, then persist the checkpoint.
	consume := func(msg clusterMsg) error {
		media = msg.m
		if clock == nil {
			clock = NewClock(msg.m.TimecodeScale, msg.m.Registry)
			if skipBefore > 0 {
				clock.Restore(ckpt.Clock)
			}
		}
		type sb struct {
			b  Block
			ns int64
		}
		stamped := make([]sb, 0, len(msg.blocks))
		for _, b := range msg.blocks {
			processed++
			if processed <= skipBefore {
				continue // already processed in a previous run
			}
			ns, err := clock.Stamp(b.Track, b.Ticks, b.Offset)
			if err != nil {
				return err
			}
			stamped = append(stamped, sb{b, ns})
		}
		sort.SliceStable(stamped, func(i, j int) bool {
			if stamped[i].ns != stamped[j].ns {
				return stamped[i].ns < stamped[j].ns
			}
			if stamped[i].b.Track != stamped[j].b.Track {
				return stamped[i].b.Track < stamped[j].b.Track
			}
			return stamped[i].b.Offset < stamped[j].b.Offset
		})
		for _, s := range stamped {
			if s.b.Offset+s.b.Size > maxOff {
				maxOff = s.b.Offset + s.b.Size
			}
			idx := seg.Index(s.ns)
			if cur != nil && idx != cur.Index {
				data, _ := json.Marshal(cur)
				sum := sha256.Sum256(data)
				ckpt.Segments = append(ckpt.Segments,
					CommittedSegment{Segment: *cur, SHA256: hex.EncodeToString(sum[:])})
				ckpt.Stage = "segments"
				cur = nil
			}
			if cur == nil {
				start, end := seg.Range(idx)
				cur = &Segment{Index: idx, Start: start, End: end}
			}
			cur.Blocks = append(cur.Blocks, BlockRef{Track: s.b.Track, Ns: s.ns, Offset: s.b.Offset, Size: s.b.Size})
			trk := msg.m.Registry.GetOrCreate(s.b.Track)
			payload, peak, err := normalizeBlock(trk, s.b)
			if err != nil {
				return err
			}
			if trk.Type == 2 { // audio
				dur := trk.DefaultDuration
				if dur <= 0 {
					dur = 20_000_000
				}
				contiguous := false
				if exp, ok := silence.Expected(s.b.Track); ok {
					contiguous = s.ns == exp
				}
				silence.Add(s.b.Track, s.ns, dur, peak, contiguous)
			}
			fp.Add(s.b.Track, s.ns, payload)
		}
		ckpt.ProcessedBlocks = processed
		ckpt.CommittedOffset = maxOff
		ckpt.Clock = clock.Snapshot()
		ckpt.Silence = silence.Snapshot()
		ckpt.Fingerprints = fp.Snapshot()
		ckpt.Partial = cur
		if err := ckpt.Save(ckptPath); err != nil {
			return err
		}
		if p.cfg.FailAfterSegments > 0 && len(ckpt.Segments) >= p.cfg.FailAfterSegments {
			return &Error{ErrInterrupted, maxOff, "",
				fmt.Sprintf("simulated interrupt after %d committed segments", len(ckpt.Segments))}
		}
		return nil
	}

	for msg := range q {
		if err := consume(msg); err != nil {
			cancel()
			return nil, err
		}
	}
	if err := <-prodErr; err != nil {
		cancel()
		return nil, err
	}
	// Commit the final open segment.
	if cur != nil {
		data, _ := json.Marshal(cur)
		sum := sha256.Sum256(data)
		ckpt.Segments = append(ckpt.Segments,
			CommittedSegment{Segment: *cur, SHA256: hex.EncodeToString(sum[:])})
		cur = nil
	}
	silence.Flush()

	man := &Manifest{
		InputSHA256:  inputSHA,
		Params:       p.Params(),
		Segments:     ckpt.Segments,
		Silence:      silence.Intervals,
		Fingerprints: fp.FPS,
		Duplicates:   fp.Duplicates,
	}
	man.OutputSHA256 = man.ComputeOutputSHA()
	ckpt.Stage = "done"
	ckpt.Partial = nil
	if err := ckpt.Save(ckptPath); err != nil {
		return nil, err
	}
	if err := man.Save(filepath.Join(p.outDir, "manifest.json")); err != nil {
		return nil, err
	}
	tracks := 0
	if media != nil {
		tracks = len(media.Registry.Tracks)
	}
	if err := WriteMP4Summary(filepath.Join(p.outDir, "summary.mp4"), Summary{
		InputSHA256: inputSHA, Tracks: tracks, Segments: len(man.Segments),
		SilenceRuns: len(man.Silence), Duplicates: man.Duplicates,
		OutputSHA256: man.OutputSHA256,
	}); err != nil {
		return nil, err
	}
	return man, nil
}

// normalizeBlock extracts the normalized media payload (RTP payload with the
// RTP header stripped) and the PCM16 peak amplitude for audio blocks.
func normalizeBlock(trk *Track, b Block) ([]byte, int, error) {
	payload := b.Data
	if strings.HasPrefix(trk.Codec, "RTP/") {
		var pkt rtp.Packet
		if err := pkt.Unmarshal(b.Data); err != nil {
			return nil, 0, &Error{ErrInvalidVINT, b.Offset, "", "rtp: " + err.Error()}
		}
		payload = pkt.Payload
	}
	peak := 0
	if trk.Type == 2 {
		for i := 0; i+2 <= len(payload); i += 2 {
			v := int(int16(binary.LittleEndian.Uint16(payload[i:])))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
	}
	return payload, peak, nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
