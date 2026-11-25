package wav

import (
	"encoding/binary"
	"io"

	"assetcheck/internal/errmodel"
)

// Config is the validated PCM/WAVE format block.
type Config struct {
	AudioFormat   uint16 `json:"audio-format"`
	Channels      int    `json:"channels"`
	SampleRate    int    `json:"sample-rate"`
	ByteRate      int    `json:"byte-rate"`
	BlockAlign    int    `json:"block-align"`
	BitsPerSample int    `json:"bits-per-sample"`
	ExtensiblePCM bool   `json:"extensible-pcm"`
}

// Wav is the parsed representation of one WAV file.
type Wav struct {
	FileSize         int64             `json:"file-size"`
	RIFFDeclaredSize uint32            `json:"riff-declared-size"`
	Config           Config            `json:"config"`
	Chunks           []ChunkInfo       `json:"chunks"`
	Findings         []*errmodel.Error `json:"findings,omitempty"`

	DataChunk        *ChunkInfo `json:"-"`
	DataOffset       int64      `json:"data-offset"`
	DeclaredDataSize int64      `json:"declared-data-size"`
	PCM              []byte     `json:"-"` // all available data bytes
	FramedPCM        []byte     `json:"-"` // whole frames only
	TailBytes        []byte     `json:"-"` // non-integral trailing bytes
	ValidFrames      int64      `json:"valid-frames"`
	DataTruncated    bool       `json:"data-truncated"`

	Info map[string]string `json:"info,omitempty"`
}

// Parse reads and validates a WAV file from memory. A non-nil error is a
// fatal format error: the returned *Wav may still carry partial information
// and offset-tagged findings for reporting.
func Parse(data []byte) (*Wav, error) {
	w := &Wav{FileSize: int64(len(data)), Info: map[string]string{}}
	if int64(len(data)) < 12 {
		return w, errmodel.New(errmodel.ErrTruncatedHeader,
			"file is %d bytes, need at least 12 for RIFF/WAVE header", len(data)).AtOffset(0)
	}
	if string(data[0:4]) != "RIFF" {
		return w, errmodel.New(errmodel.ErrBadRIFF,
			"missing RIFF magic, got %q", id4(data[0:4])).AtOffset(0)
	}
	w.RIFFDeclaredSize = binary.LittleEndian.Uint32(data[4:8])
	if string(data[8:12]) != "WAVE" {
		return w, errmodel.New(errmodel.ErrBadWAVE,
			"missing WAVE magic, got %q", id4(data[8:12])).AtOffset(8)
	}
	if int64(w.RIFFDeclaredSize)+8 != w.FileSize {
		w.Findings = append(w.Findings, errmodel.New(errmodel.ErrRIFFSizeMismatch,
			"RIFF size field %d implies %d bytes, actual file is %d",
			w.RIFFDeclaredSize, int64(w.RIFFDeclaredSize)+8, w.FileSize).AtOffset(4))
	}

	chunks, findings := WalkChunks(data, 12)
	w.Chunks = chunks
	w.Findings = append(w.Findings, findings...)

	fmtChunk := FindChunk(chunks, "fmt ")
	if fmtChunk == nil {
		return w, errmodel.New(errmodel.ErrNoFmtChunk, "no fmt chunk found").AtOffset(12)
	}
	extra, err := parseFmt(chunkPayload(data, fmtChunk), fmtChunk.DataOffset, &w.Config)
	w.Findings = append(w.Findings, extra...)
	if err != nil {
		return w, err
	}

	// Metadata (LIST/INFO) is optional.
	if l := FindChunk(chunks, "LIST"); l != nil {
		parseInfo(chunkPayload(data, l), w.Info, w.Findings)
	}

	dc := FindChunk(chunks, "data")
	if dc == nil {
		return w, errmodel.New(errmodel.ErrMissingDataChunk, "no data chunk found")
	}
	w.DataChunk = dc
	w.DataOffset = dc.DataOffset
	w.DeclaredDataSize = dc.Size
	if dc.OutOfBounds {
		w.DataTruncated = true
	}
	w.PCM = chunkPayload(data, dc)
	align := w.Config.BlockAlign
	w.ValidFrames = int64(len(w.PCM)) / int64(align)
	whole := w.ValidFrames * int64(align)
	w.FramedPCM = w.PCM[:whole]
	if int64(len(w.PCM)) > whole {
		w.TailBytes = w.PCM[whole:]
		tailOff := dc.DataOffset + whole
		w.Findings = append(w.Findings, errmodel.New(errmodel.ErrNonFrameTail,
			"%d trailing byte(s) after %d whole frame(s); blockAlign=%d",
			len(w.TailBytes), w.ValidFrames, align).AtOffset(tailOff).AtFrame(w.ValidFrames))
	}
	return w, nil
}

// ParseReader is a convenience wrapper for callers holding an io.Reader.
func ParseReader(r io.Reader) (*Wav, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, errmodel.New(errmodel.ErrChunkTruncated, "read wav: %v", err)
	}
	return Parse(b)
}

func parseFmt(p []byte, base int64, c *Config) ([]*errmodel.Error, error) {
	var extra []*errmodel.Error
	if len(p) < 16 {
		return nil, errmodel.New(errmodel.ErrBadFmtSize,
			"fmt chunk needs >=16 bytes, got %d", len(p)).AtOffset(base)
	}
	c.AudioFormat = binary.LittleEndian.Uint16(p[0:2])
	c.Channels = int(binary.LittleEndian.Uint16(p[2:4]))
	c.SampleRate = int(binary.LittleEndian.Uint32(p[4:8]))
	c.ByteRate = int(binary.LittleEndian.Uint32(p[8:12]))
	c.BlockAlign = int(binary.LittleEndian.Uint16(p[12:14]))
	c.BitsPerSample = int(binary.LittleEndian.Uint16(p[14:16]))

	formatCode := c.AudioFormat
	if formatCode == 0xFFFE {
		c.ExtensiblePCM = true
		if len(p) < 40 {
			return nil, errmodel.New(errmodel.ErrBadFmtSize,
				"WAVE_FORMAT_EXTENSIBLE fmt needs 40 bytes, got %d", len(p)).AtOffset(base)
		}
		formatCode = binary.LittleEndian.Uint16(p[24:26]) // sub-format GUID first field
	}
	if formatCode != 1 {
		return nil, errmodel.New(errmodel.ErrUnsupportedFormat,
			"only integer PCM (format=1) is supported, got format code %d", formatCode).AtOffset(base)
	}
	if c.Channels <= 0 {
		return nil, errmodel.New(errmodel.ErrBadChannels, "channels must be positive, got %d", c.Channels).
			AtOffset(base + 2)
	}
	if c.SampleRate <= 0 {
		return nil, errmodel.New(errmodel.ErrBadSampleRate, "sample rate must be positive, got %d", c.SampleRate).
			AtOffset(base + 4)
	}
	if c.BitsPerSample != 8 && c.BitsPerSample != 16 && c.BitsPerSample != 24 && c.BitsPerSample != 32 {
		return nil, errmodel.New(errmodel.ErrBadBitDepth,
			"unsupported bits per sample %d (want 8/16/24/32 integer PCM)", c.BitsPerSample).AtOffset(base + 14)
	}
	wantAlign := c.Channels * (c.BitsPerSample / 8)
	if c.BlockAlign != wantAlign {
		return nil, errmodel.New(errmodel.ErrBlockAlignMismatch,
			"blockAlign=%d but channels=%d bits=%d implies %d",
			c.BlockAlign, c.Channels, c.BitsPerSample, wantAlign).AtOffset(base + 12)
	}
	if wantRate := c.SampleRate * c.BlockAlign; c.ByteRate != wantRate {
		extra = append(extra, errmodel.New(errmodel.ErrByteRateMismatch,
			"byteRate=%d but rate*blockAlign=%d; using blockAlign for frame math",
			c.ByteRate, wantRate).AtOffset(base+8))
	}
	return extra, nil
}

func parseInfo(p []byte, info map[string]string, findings []*errmodel.Error) {
	if len(p) < 4 || id4(p[0:4]) != "INFO" {
		return
	}
	off := 4
	for off+8 <= len(p) {
		key := id4(p[off : off+4])
		n := int(binary.LittleEndian.Uint32(p[off+4 : off+8]))
		start := off + 8
		end := start + n
		if end > len(p) {
			findings = append(findings, errmodel.New(errmodel.ErrMetadataEncoding,
				"INFO tag %q declares %d bytes past LIST end", key, n).AtOffset(int64(off)))
			return
		}
		val := p[start:end]
		for len(val) > 0 && (val[len(val)-1] == 0 || val[len(val)-1] == ' ') {
			val = val[:len(val)-1]
		}
		info[key] = string(val)
		off = end + (n & 1)
	}
}
