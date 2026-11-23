package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"math"
	"os"
	"sort"
	"time"


	"mediacore/audio"
	"mediacore/ingest"
	"mediacore/media"
	"mediacore/rtp"
)

const (
	videoSSRC     = 0x01020304
	audioSSRC     = 0x05060708
	fragsPerFrame = 16 // sequence numbers are derived as frame*16+frag
	maxConceal    = 16 // loss bursts up to this many packets are concealed
	videoTicks    = 3000 // 90000 Hz / 30 fps
)

type vframe struct{ off, end, vcl int64 }

func isFrameUnit(u ingest.Unit, c ingest.Codec) bool {
	switch c {
	case ingest.CodecH264:
		return u.Type >= 1 && u.Type <= 5
	case ingest.CodecH265:
		return u.Type <= 31
	default: // av1: frame header / frame OBU
		return u.Type == 3 || u.Type == 6
	}
}

// groupFrames folds scanned units into frames: non-frame units (SPS/PPS,
// sequence headers) attach as a prefix to the next frame unit.
func groupFrames(units []ingest.Unit, c ingest.Codec) []vframe {
	var frames []vframe
	prefix := int64(-1)
	for _, u := range units {
		if isFrameUnit(u, c) {
			start := u.Offset
			if prefix >= 0 {
				start = prefix
			}
			frames = append(frames, vframe{off: start, end: u.Offset + u.Size, vcl: u.Offset})
			prefix = -1
		} else if prefix < 0 {
			prefix = u.Offset
		}
	}
	return frames
}

// rebuildIndex derives a keyframe index from the bitstream itself. Used
// to recover after a declared index is rejected and quarantined.
func rebuildIndex(stream []byte, c ingest.Codec) (*ingest.KeyframeIndex, error) {
	units, err := ingest.Scan(stream, c)
	if err != nil {
		return nil, err
	}
	idx := &ingest.KeyframeIndex{Digest: ingest.DigestOf(stream)}
	for _, u := range units {
		if u.Keyframe {
			idx.Offsets = append(idx.Offsets, u.Offset)
			idx.Sizes = append(idx.Sizes, u.Size)
		}
	}
	return idx, nil
}

func f32bytes(s []float32) []byte {
	b := make([]byte, 4*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}

func bytesF32(b []byte) []float32 {
	s := make([]float32, len(b)/4)
	for i := range s {
		s[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return s
}

func p95(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(0.95*float64(len(s)-1))]
}

// run executes the staged pipeline; see runner.go for the model.
func (r *Runner) run() (Result, error) {
	cfg := r.cfg
	res := Result{CacheSourceChecked: true}
	if err := cfg.AudioMeta.Validate(); err != nil {
		return res, err
	}

	// ---- stage: ingest ----
	idx := cfg.Index
	if idx == nil {
		built, err := rebuildIndex(cfg.Video, cfg.Codec)
		if err != nil {
			return res, err
		}
		idx = built
	} else if err := ingest.ValidateIndex(idx, cfg.Video, cfg.Codec); err != nil {
		if media.CodeOf(err) != media.CodeIndexInvalid {
			return res, err
		}
		// quarantine the declared index, rebuild from the bitstream
		res.ErrorCode, res.Recovered = media.CodeIndexInvalid, true
		idx, err = rebuildIndex(cfg.Video, cfg.Codec)
		if err != nil {
			return res, err
		}
	}
	units, err := ingest.Scan(cfg.Video, cfg.Codec)
	if err != nil {
		return res, err
	}
	frames := groupFrames(units, cfg.Codec)
	if len(frames) == 0 {
		return res, media.Fault(media.CodeNALCorrupt, "pipeline.run", -1, "no frames in stream")
	}
	keySet := make(map[int64]bool, len(idx.Offsets))
	for _, o := range idx.Offsets {
		keySet[o] = true
	}

	// ---- stage: audio setup ----
	fromLayout, err := audio.ParseLayout(cfg.AudioMeta.Layout)
	if err != nil {
		return res, err
	}
	tch := cfg.TargetLayout.Channels()
	resampler, err := audio.NewResampler(cfg.AudioMeta.Rate, cfg.TargetRate, tch)
	if err != nil {
		return res, err
	}
	limiter := audio.NewLimiter(tch, cfg.TargetRate/10, 0.2, 0.95)
	audioTicks := int64(cfg.TargetRate / FPS)

	// ---- resume or fresh start ----
	file, hasher, outBytes, start, seqNo, err := r.openOutput()
	if err != nil {
		return res, err
	}
	defer file.Close()
	out := io.MultiWriter(file, hasher)

	recV := rtp.NewRecover(0, maxConceal)
	recA := rtp.NewRecover(0, maxConceal)
	pktV := rtp.NewPacketizer(videoSSRC, 96, media.VideoScale)
	pktA := rtp.NewPacketizer(audioSSRC, 97, media.TimeScale(cfg.TargetRate))
	var tracker ingest.ColorTracker
	gopIdx := 0
	prevKey := int64(-1)

	audioChunk := func(i int) []float32 {
		ch := int64(cfg.AudioMeta.Channels)
		s := int64(i) * int64(cfg.AudioMeta.Rate) / FPS
		e := int64(i+1) * int64(cfg.AudioMeta.Rate) / FPS
		total := int64(len(cfg.Audio)) / ch
		if e > total {
			e = total
		}
		if s > total {
			s = total
		}
		return cfg.Audio[s*ch : e*ch]
	}
	processAudio := func(i int) ([]byte, error) {
		norm, err := audio.Normalize(audioChunk(i), fromLayout, cfg.TargetLayout)
		if err != nil {
			return nil, err
		}
		// DSP state always advances so a cache hit never forks the timeline.
		rs, err := resampler.Process(norm)
		if err != nil {
			return nil, err
		}
		fresh := f32bytes(limiter.Process(rs))
		key := sha256.Sum256(f32bytes(norm))
		cached, cerr := cfg.Cache.Get(cfg.SourceID, key)
		if cerr != nil {
			// forged/poisoned entry quarantined by cache: recompute path
			cfg.Cache.PutFor(key, cfg.SourceID, fresh)
			return fresh, nil
		}
		if cached != nil {
			return cached, nil
		}
		cfg.Cache.PutFor(key, cfg.SourceID, fresh)
		return fresh, nil
	}

	// On resume, rebuild DSP/color/recovery state by replaying input
	// (without emitting output) up to the checkpoint boundary.
	if start > 0 {
		var lastPCM []byte
		for i := 0; i < start; i++ {
			if keySet[frames[i].vcl] && gopIdx < len(cfg.Color) {
				tracker.Observe(cfg.Color[gopIdx], int64(i), prevKey)
				prevKey, gopIdx = int64(i), gopIdx+1
			}
			pcm, err := processAudio(i)
			if err != nil {
				return res, err
			}
			lastPCM = pcm
		}
		pktV.SetNextSeq(uint16((start - 1) * fragsPerFrame))
		vts := media.Ticks(int64(start-1) * videoTicks)
		vlast, err := pktV.Packetize(cfg.Video[frames[start-1].off:frames[start-1].end], vts)
		if err != nil {
			return res, err
		}
		recV.Seed(vlast[len(vlast)-1], int64(vts), int64(start*fragsPerFrame))
		pktA.SetNextSeq(uint16((start - 1) * fragsPerFrame))
		ats := media.Ticks(int64(start-1) * audioTicks)
		alast, err := pktA.Packetize(lastPCM, ats)
		if err != nil {
			return res, err
		}
		recA.Seed(alast[len(alast)-1], int64(ats), int64(start*fragsPerFrame))
	}

	vPTS := make([]int64, len(frames)+1)
	aPTS := make([]int64, len(frames)+1)
	for i := range vPTS {
		vPTS[i], aPTS[i] = -1, -1
	}
	var lat []int64
	emit := func(items []rtp.Recovered, kind byte) error {
		for _, it := range items {
			payload := it.Packet.Payload
			var rec [5]byte
			rec[0] = kind
			binary.LittleEndian.PutUint32(rec[1:], uint32(len(payload)))
			if _, err := out.Write(rec[:]); err != nil {
				return media.Wrap(media.CodeInternal, "pipeline.emit", -1, err, "record header")
			}
			if _, err := out.Write(payload); err != nil {
				return media.Wrap(media.CodeInternal, "pipeline.emit", -1, err, "record payload")
			}
			outBytes += int64(5 + len(payload))
			f := int(it.Seq) / fragsPerFrame
			if kind == 'V' && f < len(vPTS) && vPTS[f] < 0 {
				vPTS[f] = it.TS
			}
			if kind == 'A' && f < len(aPTS) && aPTS[f] < 0 {
				aPTS[f] = it.TS
			}
		}
		return nil
	}

	// ---- stage: per-frame rtp/output ----
	for i := start; i < len(frames); i++ {
		t0 := time.Now()
		pktV.SetNextSeq(uint16(i * fragsPerFrame))
		vts := media.Ticks(int64(i) * videoTicks)
		pkts, err := pktV.Packetize(cfg.Video[frames[i].off:frames[i].end], vts)
		if err != nil {
			return res, err
		}
		if len(pkts) > fragsPerFrame {
			return res, media.Fault(media.CodeLimitExceeded, "pipeline.run", -1,
				"frame %d needs %d fragments > %d", i, len(pkts), fragsPerFrame)
		}
		if cfg.Wire != nil {
			pkts = cfg.Wire(i, pkts)
		}
		for _, p := range pkts {
			if err := emit(recV.Push(p), 'V'); err != nil {
				return res, err
			}
		}
		pcm, err := processAudio(i)
		if err != nil {
			return res, err
		}
		pktA.SetNextSeq(uint16(i * fragsPerFrame))
		apkts, err := pktA.Packetize(pcm, media.Ticks(int64(i)*audioTicks))
		if err != nil {
			return res, err
		}
		if len(apkts) > fragsPerFrame {
			return res, media.Fault(media.CodeLimitExceeded, "pipeline.run", -1,
				"audio chunk %d needs %d fragments > %d", i, len(apkts), fragsPerFrame)
		}
		if cfg.Wire != nil {
			apkts = cfg.Wire(i, apkts)
		}
		for _, p := range apkts {
			if err := emit(recA.Push(p), 'A'); err != nil {
				return res, err
			}
		}
		if keySet[frames[i].vcl] && gopIdx < len(cfg.Color) {
			tracker.Observe(cfg.Color[gopIdx], int64(i), prevKey)
			prevKey, gopIdx = int64(i), gopIdx+1
		}
		lat = append(lat, time.Since(t0).Milliseconds())
		if keySet[frames[i].vcl] && cfg.Store != nil {
			cp := Checkpoint{Seq: seqNo, Stage: "frame", Offset: int64(i + 1),
				OutBytes: outBytes, Digest: hex.EncodeToString(hasher.Sum(nil)),
				ColorState: tracker.State().String()}
			if err := cfg.Store.Save(cp); err != nil {
				return res, err
			}
			seqNo++
		}
		if cfg.CrashAfter > 0 && i+1 == cfg.CrashAfter {
			res.Crashed = true
			return res, nil
		}
	}

	// ---- tail: resampler flush, recovery flush, trailer ----
	if tail := resampler.Flush(); len(tail) > 0 {
		pcm := f32bytes(limiter.Process(tail))
		pktA.SetNextSeq(uint16(len(frames) * fragsPerFrame))
		apkts, err := pktA.Packetize(pcm, media.Ticks(int64(len(frames))*audioTicks))
		if err != nil {
			return res, err
		}
		if cfg.Wire != nil {
			apkts = cfg.Wire(len(frames), apkts)
		}
		for _, p := range apkts {
			if err := emit(recA.Push(p), 'A'); err != nil {
				return res, err
			}
		}
	}
	if err := emit(recV.Flush(), 'V'); err != nil {
		return res, err
	}
	if err := emit(recA.Flush(), 'A'); err != nil {
		return res, err
	}
	colorSummary, cerr := tracker.Summary()
	if cerr != nil {
		// conflict propagates to the summary and the result, run completes
		if res.ErrorCode == "" {
			res.ErrorCode = media.CodeOf(cerr)
		}
	}
	tb := []byte(colorSummary)
	var th [5]byte
	th[0] = 'T'
	binary.LittleEndian.PutUint32(th[1:], uint32(len(tb)))
	if _, err := out.Write(th[:]); err != nil {
		return res, media.Wrap(media.CodeInternal, "pipeline.run", -1, err, "trailer")
	}
	if _, err := out.Write(tb); err != nil {
		return res, media.Wrap(media.CodeInternal, "pipeline.run", -1, err, "trailer")
	}
	outBytes += int64(5 + len(tb))

	res.OutputDigest = hex.EncodeToString(hasher.Sum(nil))
	if cfg.Store != nil {
		if err := cfg.Store.Save(Checkpoint{Seq: seqNo, Stage: "done", Offset: int64(len(frames)),
			OutBytes: outBytes, Digest: res.OutputDigest, ColorState: tracker.State().String()}); err != nil {
			return res, err
		}
	}
	if err := file.Close(); err != nil {
		return res, media.Wrap(media.CodeInternal, "pipeline.run", -1, err, "close output")
	}
	if err := writeContainer(cfg.OutPath, cfg.OutPath+".mp4", tb); err != nil {
		return res, err
	}

	// ---- metrics ----
	var skews []int64
	for i := range frames {
		if vPTS[i] >= 0 && aPTS[i] >= 0 {
			d := vPTS[i]/90 - aPTS[i]*1000/int64(cfg.TargetRate)
			if d < 0 {
				d = -d
			}
			skews = append(skews, d)
		}
	}
	res.AVSyncP95Ms = p95(skews)
	res.LatencyP95Ms = p95(lat)
	rec := recV.Stats.Recovered + recA.Stats.Recovered
	lost := recV.Stats.Lost + recA.Stats.Lost
	res.LossRecoveryPct = 100
	if rec+lost > 0 {
		res.LossRecoveryPct = 100 * float64(rec) / float64(rec+lost)
	}
	res.VideoFrames = len(frames)
	res.AudioOutFrames = resampler.OutFrames()
	res.VideoStats, res.AudioStats = recV.Stats, recA.Stats
	res.InPeak, res.OutPeak, res.LastGain = limiter.InPeak, limiter.OutPeak, limiter.LastGain
	res.ColorSummary = colorSummary
	return res, nil
}

// openOutput prepares the output file and hash state. Resume support
// lands in the next patch; for now it always starts fresh.
func (r *Runner) openOutput() (*os.File, hash.Hash, int64, int, int, error) {
	return nil, nil, 0, 0, 0, media.Fault(media.CodeInternal, "pipeline.openOutput", -1, "not implemented")
}

// writeContainer wraps the record stream in a minimal MP4 container.
// Implemented in the next patch.
func writeContainer(recordsPath, mp4Path string, meta []byte) error {
	return nil
}
