package wav

import (
	"mediad/media"
)

// Supported WAVE format tags: 1 = PCM, 0xFFFE = WAVE_FORMAT_EXTENSIBLE
// (the SubFormat GUID's data1 still carries the underlying tag as PCM).
const (
	formatPCM        uint16 = 1
	formatExtensible uint16 = 0xFFFE
)

// PCMInfo is the validated geometry of an LPCM stream.
type PCMInfo struct {
	Channels      uint16
	SampleRate    uint32
	BitsPerSample uint16
	BlockAlign    uint16
	FrameBytes    int64
}

// TimeBase returns the unified media time base for this PCM stream. The RTP
// clock is left unset here and is supplied by the RTP layer.
func (p PCMInfo) TimeBase() media.TimeBase {
	return media.TimeBase{SampleRate: p.SampleRate}
}

// PCMResult combines geometry, payload range and frame accounting.
type PCMResult struct {
	Info        PCMInfo
	Payload     []byte // aliases the raw buffer, never copied
	DataStart   int64
	DataEnd     int64
	FrameCount  int64
	Partial     int64 // trailing bytes that do not complete a frame
	Faults      []media.Fault
}

// ValidatePCM cross-checks fmt fields against the data payload. It requires a
// parsed fmt chunk; structural faults already recorded by ParseChunks are
// passed through unchanged so the caller keeps one fault list.
func ValidatePCM(f *File, structural []media.Fault) *PCMResult {
	res := &PCMResult{Faults: append([]media.Fault(nil), structural...)}
	if f.Fmt == nil || f.Data == nil {
		return res
	}
	fc := f.Fmt
	if fc.AudioFormat != formatPCM && fc.AudioFormat != formatExtensible {
		res.Faults = append(res.Faults, media.NewByteFault(
			media.CatBadContainer, fc.DataStart, fc.DataStart+2,
			"unsupported audio format tag %d (want PCM 1 or extensible 0xFFFE)", fc.AudioFormat))
	}
	validateFmtFields(f, res)

	frameBytes := int64(fc.Channels) * int64(fc.BitsPerSample) / 8
	info := PCMInfo{
		Channels:      fc.Channels,
		SampleRate:    fc.SampleRate,
		BitsPerSample: fc.BitsPerSample,
		BlockAlign:    fc.BlockAlign,
		FrameBytes:    frameBytes,
	}
	res.Info = info

	// The payload is bounded by the walk limit: an out-of-bounds data chunk
	// never hands out bytes past the actual buffer.
	end := f.Data.DataEnd
	if end > f.Limit {
		end = f.Limit
	}
	res.DataStart = f.Data.DataStart
	res.DataEnd = end
	if int(f.Data.DataStart) <= len(f.Raw()) && int(end) <= len(f.Raw()) {
		res.Payload = f.Raw()[f.Data.DataStart:end]
	}

	if frameBytes <= 0 {
		// Zero channels or bit depth: every byte is unframed.
		if end > f.Data.DataStart {
			res.Faults = append(res.Faults, media.NewByteFault(
				media.CatPartialFrame, f.Data.DataStart, end,
				"cannot frame data: frame size is zero"))
			res.Partial = end - f.Data.DataStart
		}
		return res
	}

	avail := end - f.Data.DataStart
	res.FrameCount = avail / frameBytes
	res.Partial = avail % frameBytes
	if res.Partial != 0 {
		ps := f.Data.DataStart + res.FrameCount*frameBytes
		res.Faults = append(res.Faults, media.NewByteFault(
			media.CatPartialFrame, ps, end,
			"trailing %d bytes do not complete a %d-byte frame", res.Partial, frameBytes))
	}
	return res
}

// validateFmtFields checks the relationships the fmt chunk promises:
// non-zero geometry, block alignment and average byte rate.
func validateFmtFields(f *File, res *PCMResult) {
	fc := f.Fmt
	p := fc.DataStart
	checks := []struct {
		cat   media.Category
		start int64
		end   int64
		bad   bool
		msg   string
		args  []any
	}{
		{media.CatBadContainer, p + 2, p + 4, fc.Channels == 0,
			"channel count must be non-zero", nil},
		{media.CatBadContainer, p + 4, p + 8, fc.SampleRate == 0,
			"sample rate must be non-zero", nil},
		{media.CatBadContainer, p + 14, p + 16, fc.BitsPerSample == 0 || fc.BitsPerSample%8 != 0,
			"bits per sample %d is not a positive multiple of 8", []any{fc.BitsPerSample}},
	}
	for _, c := range checks {
		if c.bad {
			res.Faults = append(res.Faults, media.NewByteFault(c.cat, c.start, c.end, c.msg, c.args...))
		}
	}
	if fc.Channels != 0 && fc.BitsPerSample != 0 {
		wantAlign := fc.Channels * (fc.BitsPerSample / 8)
		if fc.BlockAlign != wantAlign {
			res.Faults = append(res.Faults, media.NewByteFault(
				media.CatBadContainer, p+12, p+14,
				"block align %d disagrees with channels*depth/8 = %d", fc.BlockAlign, wantAlign))
		}
		wantRate := fc.SampleRate * uint32(wantAlign)
		if fc.ByteRate != wantRate {
			res.Faults = append(res.Faults, media.NewByteFault(
				media.CatBadContainer, p+8, p+12,
				"byte rate %d disagrees with rate*blockAlign = %d", fc.ByteRate, wantRate))
		}
	}
}

// Raw exposes the original buffer so callers can slice payloads without
// ParseChunks having to retain it on the File value.
func (f *File) Raw() []byte { return f.raw }
