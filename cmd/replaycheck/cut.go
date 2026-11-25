package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"replaycheck/internal/clip"
	"replaycheck/internal/clock"
	"replaycheck/internal/fault"
	"replaycheck/internal/metrics"
	"replaycheck/internal/repair"
	"replaycheck/internal/stream"
	"replaycheck/internal/wav"
)

// cutAll cuts every event, writes the fragments atomically and returns the
// per-event statistics that the manifest will publish.
func cutAll(cfg RunConfig, wavR *wav.Reader, mp4F *os.File, gaps []repair.Gap) ([]ClipStats, error) {
	if _, err := mp4F.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	info, err := stream.ParseFile(mp4F, cfg.MP4Path, fileSize(mp4F))
	if err != nil {
		return nil, err
	}
	cut, err := clip.New(&clip.Source{
		WAV:      wavR,
		Stream:   info,
		TickRate: clock.MustRate(int64(info.Track.Timescale), 1),
	})
	if err != nil {
		return nil, err
	}
	ix := repair.New(gaps)
	outClips := filepath.Join(cfg.OutDir, "clips", cfg.TaskKey)
	if err := os.MkdirAll(outClips, 0o755); err != nil {
		return nil, err
	}

	stats := make([]ClipStats, 0, len(cfg.Events))
	for _, ev := range cfg.Events {
		sl, err := cut.Cut(ev)
		if err != nil {
			return nil, err
		}
		// Refuse before writing anything: the payload is not on disk, and a
		// short fragment would misrepresent the cut.
		if g, ok := ix.OverlapsSource(sl.Video.StartTick, sl.Video.EndTick, "mp4"); ok {
			return nil, fault.New(fault.KindUnreadable, fault.Fatal,
				fault.Location{ByteOffset: g.ByteOffset, Tick: g.Start, HasTick: true,
					Millis: g.Start * 1000 / int64(info.Track.Timescale), Label: "mp4"},
				"event %q needs sample payload missing at byte %d (tick %d)",
				ev.Name, g.ByteOffset, g.Start)
		}

		audioName := safeName(ev.Name) + ".wav"
		videoName := safeName(ev.Name) + ".h264"
		audioPath := filepath.Join(outClips, audioName)
		videoPath := filepath.Join(outClips, videoName)
		audioDigest, err := writeAudioFragment(wavR, cut, sl, audioPath)
		if err != nil {
			return nil, fmt.Errorf("event %q audio fragment: %w", ev.Name, err)
		}
		videoBytes, err := cut.SliceBytes(sl, io.NewSectionReader(mp4F, 0, fileSize(mp4F)))
		if err != nil {
			return nil, fmt.Errorf("event %q video fragment: %w", ev.Name, err)
		}
		if err := writeAtomic(videoPath, videoBytes); err != nil {
			return nil, err
		}
		clipSum, err := canonicalDigest(audioDigest, len(audioName), videoBytes)
		if err != nil {
			return nil, err
		}

		peak, err := wavR.Peak(sl.Audio.StartFrame, sl.Audio.Frames)
		if err != nil {
			return nil, err
		}
		damaged := false
		if g, ok := ix.Overlaps(sl.Audio.StartTick, sl.Audio.EndTick); ok {
			damaged = true
			sl.Issues.Add(fault.New(g.Kind, fault.Damaged,
				fault.Location{ByteOffset: g.ByteOffset, Tick: sl.Audio.StartTick, HasTick: true},
				"audio interval overlaps %s damage", g.Source))
		}
		if g, ok := ix.OverlapsSource(sl.Video.StartTick, sl.Video.EndTick, "rtp"); ok {
			damaged = true
			sl.Issues.Add(fault.New(g.Kind, fault.Damaged,
				fault.Location{ByteOffset: g.ByteOffset, Tick: sl.Video.StartTick, HasTick: true},
				"video interval overlaps transport damage"))
		}
		st := ClipStats{
			TaskKey:     cfg.TaskKey + "/" + ev.Name,
			Event:       ev.Name,
			AudioStart:  sl.Audio.StartFrame,
			AudioEnd:    sl.Audio.EndFrame,
			AudioFrames: sl.Audio.Frames,
			VideoFirst:  sl.Video.FirstSample,
			VideoLast:   sl.Video.LastSample,
			LeadInMs:    sl.Video.LeadInMs,
			LeadInTick:  sl.Video.LeadInTicks,
			ClipSHA256:  clipSum,
			FrameHash:   metrics.HashSamples(splitSamples(videoBytes, info, sl)),
			PixelMean:   metrics.FormatFloat(metrics.MeanBytes(videoBytes)),
			AudioPeak:   metrics.FormatFloat(peak),
			Damaged:     damaged,
			Bytes:       sl.Audio.Bytes + int64(len(videoBytes)),
			Path:        filepath.Join("clips", cfg.TaskKey, audioName),
		}
		stats = append(stats, st)
	}
	return stats, nil
}

// writeAudioFragment streams the slice's PCM into a standalone WAV file via
// a temporary file and returns the SHA-256 of the written bytes. The
// fragment is never held in memory, so long events stay cheap.
func writeAudioFragment(wavR *wav.Reader, cut *clip.Cutter, sl *clip.Slice, path string) (string, error) {
	lay := wavR.Layout
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".audio-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer os.Remove(name)

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	if _, err := wav.WritePCMHeader(mw, lay.Format.Channels, lay.Format.SampleRate,
		lay.Format.BitsPerSam, sl.Audio.Bytes); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := cut.CopyAudio(sl, mw); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(name, path); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// splitSamples cuts a concatenated payload back into per-sample slices using
// the sample table, for frame hashing.
func splitSamples(video []byte, info *stream.Info, sl *clip.Slice) [][]byte {
	if sl.Video.FirstSample < 0 {
		return nil
	}
	out := make([][]byte, 0, sl.Video.Samples)
	off := 0
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		n := int(info.Track.Samples[i].Size)
		if off+n > len(video) {
			n = len(video) - off
		}
		if n <= 0 {
			break
		}
		out = append(out, video[off:off+n])
		off += n
	}
	return out
}

// canonicalDigest hashes the two fragments with explicit framing so the
// clip digest is unambiguous about channel boundaries.
func canonicalDigest(audioHash string, audioNameLen int, video []byte) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "audio-sha256:%s\naudio-name-len:%d\nvideo:%d\n", audioHash, audioNameLen, len(video))
	if _, err := h.Write(video); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".clip-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fileSize(f *os.File) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

func safeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "event"
	}
	return string(out)
}
