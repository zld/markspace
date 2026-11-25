package main

import (
	"bytes"
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
		audioBytes, err := audioFragment(wavR, sl)
		if err != nil {
			return nil, err
		}
		videoBytes, err := cut.SliceBytes(sl, io.NewSectionReader(mp4F, 0, fileSize(mp4F)))
		if err != nil {
			return nil, err
		}

		audioName := safeName(ev.Name) + ".wav"
		videoName := safeName(ev.Name) + ".h264"
		audioPath := filepath.Join(outClips, audioName)
		videoPath := filepath.Join(outClips, videoName)
		if err := writeAtomic(audioPath, audioBytes); err != nil {
			return nil, err
		}
		if err := writeAtomic(videoPath, videoBytes); err != nil {
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

		clipSum := metrics.HashBytes(canonical(audioBytes, videoBytes))
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
			Bytes:       int64(len(audioBytes) + len(videoBytes)),
			Path:        filepath.Join("clips", cfg.TaskKey, audioName),
		}
		stats = append(stats, st)
	}
	return stats, nil
}

// audioFragment renders the slice as a standalone WAV image.
func audioFragment(wavR *wav.Reader, sl *clip.Slice) ([]byte, error) {
	lay := wavR.Layout
	frames := sl.Audio.Frames
	if frames == 0 {
		return wav.BuildPCM(lay.Format.Channels, lay.Format.SampleRate, lay.Format.BitsPerSam, nil), nil
	}
	pcm, err := wavR.ReadFrames(sl.Audio.StartFrame, frames)
	if err != nil {
		return nil, err
	}
	return wav.BuildPCM(lay.Format.Channels, lay.Format.SampleRate, lay.Format.BitsPerSam, pcm), nil
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

// canonical concatenates the two fragment payloads with explicit lengths so
// the clip digest is unambiguous.
func canonical(audio, video []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "audio:%d\n", len(audio))
	b.Write(audio)
	fmt.Fprintf(&b, "\nvideo:%d\n", len(video))
	b.Write(video)
	return b.Bytes()
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
