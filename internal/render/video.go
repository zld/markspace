package render

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"

	"replayctl/internal/fault"
	mp4scan "replayctl/internal/mp4"
	"replayctl/internal/timebase"
	"replayctl/internal/timeline"
)

func activeAtTick(tl timeline.Timeline, rate uint32, t int64) ([]tickClip, error) {
	var out []tickClip
	for _, c := range tl.Clips {
		tc, err := clipTicks(c, rate)
		if err != nil {
			return nil, err
		}
		if t >= tc.start && t < tc.end {
			out = append(out, tc)
		}
	}
	return out, nil
}

func transitionAtTick(tl timeline.Timeline, rate uint32, t int64) (timeline.Transition, int64, int64, bool) {
	for _, tr := range tl.Transitions {
		s, e, err := tr.BoundsTicks(rate)
		if err == nil && t >= s && t < e {
			return tr, s, e, true
		}
	}
	return timeline.Transition{}, 0, 0, false
}

func (r *Renderer) sourceFrame(tc tickClip, t int64) (image.Image, mp4scan.Sample, error) {
	src, err := r.sourceFor(tc.clip)
	if err != nil {
		return nil, mp4scan.Sample{}, err
	}
	sourceTick := tc.sourceIn + (t - tc.start)
	if src.Index.Video == nil {
		return nil, mp4scan.Sample{}, fault.New(fault.Unsupported, fault.StageRender, src.Path, -1, sourceTick, "no video index")
	}
	samples := src.Index.Video.SamplesIn(sourceTick, sourceTick+timebase.FrameDuration)
	if len(samples) == 0 {
		rap, _ := src.Index.Video.Slice(sourceTick, sourceTick+timebase.FrameDuration)
		_ = rap
		return nil, mp4scan.Sample{}, fault.New(fault.Render, fault.StageRender, src.Path, -1, sourceTick, "no video sample at tick")
	}
	smp := samples[0]
	data, err := src.ReadJPEG(int(smp.Index))
	if err != nil {
		return nil, smp, err
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, smp, fault.New(fault.Render, fault.StageRender, src.Path, smp.Offset, smp.DTS, "decode JPEG: %v", err)
	}
	return img, smp, nil
}

func copyImage(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(x-b.Min.X, y-b.Min.Y, img.At(x, y))
		}
	}
	return out
}

func blendImages(a, b image.Image, num, den int64) *image.RGBA {
	ba, bb := a.Bounds(), b.Bounds()
	w := min(ba.Dx(), bb.Dx())
	h := min(ba.Dy(), bb.Dy())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ca := color.NRGBAModel.Convert(a.At(ba.Min.X+x, ba.Min.Y+y)).(color.NRGBA)
			cb := color.NRGBAModel.Convert(b.At(bb.Min.X+x, bb.Min.Y+y)).(color.NRGBA)
			mix := func(x, y uint8) uint8 {
				return uint8((int64(x)*(den-num) + int64(y)*num) / den)
			}
			c := color.NRGBA{R: mix(ca.R, cb.R), G: mix(ca.G, cb.G), B: mix(ca.B, cb.B), A: 255}
			out.Set(x, y, c)
		}
	}
	return out
}

func (r *Renderer) renderVideo(tl timeline.Timeline, start, end int64) (mp4scan.TrackIn, int, int64, int64, error) {
	first := ((start + timebase.FrameDuration - 1) / timebase.FrameDuration) * timebase.FrameDuration
	var data bytes.Buffer
	var sizes []uint32
	var sync []uint32
	frames := 0
	var pixels int64
	var width, height uint16

	for t := first; t < end; t += timebase.FrameDuration {
		active, err := activeAtTick(tl, timebase.VideoRate, t)
		if err != nil {
			return mp4scan.TrackIn{}, 0, 0, first, err
		}
		if len(active) == 0 {
			return mp4scan.TrackIn{}, 0, 0, first, fault.New(fault.TimeGap, fault.StageRender, "", -1, t, "no video clip at tick")
		}
		var out *image.RGBA
		if tr, ts, te, ok := transitionAtTick(tl, timebase.VideoRate, t); ok && len(active) >= 2 {
			var from, to *tickClip
			for i := range active {
				if active[i].clip.ID == tr.FromID {
					from = &active[i]
				}
				if active[i].clip.ID == tr.ToID {
					to = &active[i]
				}
			}
			if from == nil || to == nil {
				return mp4scan.TrackIn{}, 0, 0, first, fault.New(fault.Render, fault.StageRender, "", -1, t, "transition clips missing")
			}
			ia, _, err := r.sourceFrame(*from, t)
			if err != nil {
				return mp4scan.TrackIn{}, 0, 0, first, err
			}
			ib, _, err := r.sourceFrame(*to, t)
			if err != nil {
				return mp4scan.TrackIn{}, 0, 0, first, err
			}
			num, den := timeline.AlphaFraction(t, ts, te)
			out = blendImages(ia, ib, num, den)
		} else {
			img, _, err := r.sourceFrame(active[0], t)
			if err != nil {
				return mp4scan.TrackIn{}, 0, 0, first, err
			}
			out = copyImage(img)
		}

		b := out.Bounds()
		pixels += int64(b.Dx() * b.Dy())
		width, height = uint16(b.Dx()), uint16(b.Dy())
		var jb bytes.Buffer
		if err := jpeg.Encode(&jb, out, &jpeg.Options{Quality: 90}); err != nil {
			return mp4scan.TrackIn{}, 0, 0, first, fault.New(fault.Render, fault.StageRender, "", -1, t, "encode JPEG: %v", err)
		}
		sizes = append(sizes, uint32(jb.Len()))
		sync = append(sync, uint32(frames+1))
		data.Write(jb.Bytes())
		frames++
	}
	if frames == 0 {
		return mp4scan.TrackIn{}, 0, 0, first, fault.New(fault.Validation, fault.StageRender, "", -1, start, "window contains no video frames")
	}
	track := mp4scan.TrackIn{
		Kind: "video", Codec: "jpeg ", Timescale: timebase.VideoRate,
		Width: width, Height: height, Data: data.Bytes(), SampleSizes: sizes,
		SampleCount: uint32(frames), Delta: timebase.FrameDuration, SyncSamples: sync,
	}
	return track, frames, pixels, first, nil
}
