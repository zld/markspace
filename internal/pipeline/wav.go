package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"liveregress/internal/fault"
	"liveregress/internal/fixture"
	"liveregress/internal/manifest"
	"liveregress/internal/track"
	"liveregress/internal/wav"
)

// audioCheck verifies one audio rendition against its source WAV: the PCM
// frames selected by the explicit channel map must equal the remuxed sample
// bytes, so channel mapping cannot silently reorder or drop audio.
type audioCheck struct {
	Layer           string
	Path            string
	Channels        int
	SourceChannels  int
	Frames          int64
	MatchedBytes    int64
	Open            bool
	TruncatedSource bool
}

// verifyWAVMapping pairs each committed segment with its source WAV, builds
// the explicit track mapping for that layer, and confirms the remuxed PCM
// payload equals the mapped source frames byte-for-byte.
func (r *Runner) verifyWAVMapping(lr *LayerReport, pl *manifest.Playlist) (*audioCheck, error) {
	check := &audioCheck{Layer: lr.Layer, Path: filepath.ToSlash(filepath.Join(filepath.Dir(pl.Path), "seg*.wav"))}
	any := false
	for i, sr := range lr.Segments {
		if !sr.Complete || i >= len(pl.Segments) {
			continue
		}
		wavRel := filepath.ToSlash(filepath.Join(filepath.Dir(pl.Path),
			fmt.Sprintf(fixture.WAVPattern, i+1)))
		fullPath := filepath.Join(r.opts.Root, wavRel)
		if _, err := os.Stat(fullPath); err != nil {
			continue // no source WAV for this fixture; nothing to map against
		}
		check.Path = wavRel
		any = true
		if err := r.verifySegmentWAV(lr, sr, fullPath, wavRel, check); err != nil {
			return check, err
		}
	}
	if !any {
		return nil, nil
	}
	check.Open = true
	return check, nil
}

func (r *Runner) verifySegmentWAV(lr *LayerReport, sr SegmentReport, fullPath, wavRel string, check *audioCheck) error {
	info, werr := wav.Open(fullPath, wavRel)
	if werr != nil {
		fe, ok := fault.As(werr)
		if ok && fe.Kind == fault.KindPath {
			return werr
		}
		if !ok || info == nil {
			// The header could not be trusted at all (e.g. a channel/align
			// conflict): the explicit mapping cannot be satisfied.
			offset := int64(-1)
			detail := "source WAV cannot be trusted for explicit channel mapping"
			if ok {
				offset = fe.Offset
				detail += ": " + fe.Detail
			}
			return fault.New(fault.KindMapping, wavRel, offset, detail).Recover(false)
		}
		if !fe.Recoverable {
			// An invalid source WAV is a mapping refusal: the explicit
			// mapping rules cannot be satisfied.
			return fault.New(fault.KindMapping, wavRel, fe.Offset,
				"source WAV cannot satisfy the explicit channel mapping: "+fe.Detail).Recover(false)
		}
		// A truncated source WAV is compared over its complete prefix only;
		// the remuxed segment keeps exactly those frames.
		check.TruncatedSource = true
	}
	src := track.Source{ID: 1, Name: lr.Layer, Channels: info.Channels,
		SampleRate: int(info.SampleRate), Kind: "audio", Layer: lr.Layer, Path: wavRel}
	plan, merr := track.Build([]track.Source{src}, []track.Rule{{
		SourceID: 1, Layer: lr.Layer, Role: track.Role(lr.Layer),
		ChannelMap: identityChannels(info.Channels), ExpectChannels: info.Channels,
	}})
	if merr != nil {
		return merr
	}
	mapping := plan.Mappings[0]
	check.Channels = mapping.OutputChannels
	check.SourceChannels = info.Channels

	rd, rerr := wav.NewReader(fullPath, wavRel, info)
	if rerr != nil {
		return rerr
	}
	defer rd.Close()
	outPath := filepath.Join(r.outRoot, lr.Layer, filepath.Base(sr.Path))
	payload, perr := readPayload(outPath)
	if perr != nil {
		return perr
	}
	for off := 0; off+info.BlockAlign <= len(payload); off += info.BlockAlign {
		frame, err := rd.Next()
		if err != nil {
			return fault.Wrap(fault.KindTruncate, wavRel, int64(off),
				"source wav ended before remuxed audio", err).Recover(true)
		}
		want, err := mapFrame(&mapping, info, frame)
		if err != nil {
			return err
		}
		got := payload[off : off+info.BlockAlign]
		if string(want) != string(got) {
			return fault.New(fault.KindVerify, sr.Path, int64(off),
				"remuxed PCM does not match the source WAV after channel mapping").Recover(false)
		}
		check.MatchedBytes += int64(len(got))
		check.Frames++
	}
	return nil
}

// mapFrame reorders one source frame's channels per the explicit map.
func mapFrame(m *track.Mapping, info *wav.Info, fr *wav.SampleFrame) ([]byte, error) {
	if len(m.ChannelMap) != info.Channels {
		return nil, fault.New(fault.KindMapping, info.Path, fr.Offset,
			fmt.Sprintf("channel map has %d entries for a %d-channel source",
				len(m.ChannelMap), info.Channels)).Recover(false).WithTrack(1, "")
	}
	out := make([]byte, 0, len(m.ChannelMap)*info.BytesPerSample)
	for _, c := range m.ChannelMap {
		s := info.Sample(fr, c)
		if s == nil {
			return nil, fault.New(fault.KindMapping, info.Path, fr.Offset,
				fmt.Sprintf("channel %d not present in source", c)).Recover(false)
		}
		out = append(out, s...)
	}
	return out, nil
}

func identityChannels(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
