// Package wav reads RIFF and RF64 WAVE files containing integer PCM. Every
// sample frame retains its raw byte offset in the input file so a remuxer can
// copy bytes verbatim instead of decoding and re-encoding.
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// Format tags understood by this reader. Everything else is rejected with a
// named unsupported-encoding fault.
const (
	formatPCM uint16 = 1      // WAVE_FORMAT_PCM
	formatExt uint16 = 0xFFFE // WAVE_FORMAT_EXTENSIBLE
)

// Unsupported formats that have a stable name in diagnostics.
var formatNames = map[uint16]string{
	0x0002: "MS ADPCM",
	0x0011: "IMA ADPCM",
	0x0031: "GSM 6.10",
	0x0055: "MPEG Layer 3",
	0x2000: "AC-3",
	0x2001: "DTS",
	0xFFFE: "WAVE_FORMAT_EXTENSIBLE",
}

// Info is the parsed fmt chunk plus the data extent.
type Info struct {
	Path          string
	RF64          bool
	FormatTag     uint16
	Channels      int
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    int
	BitsPerSample int
	// BitsPerSampleContainer is the number of bits actually stored per sample,
	// i.e. BitsPerSample rounded up to a byte boundary. PCM24 stores 24.
	BytesPerSample int
	DataOffset     int64
	DataSize       int64 // declared by the chunk header
	Available      int64 // bytes actually present after DataOffset
	Frames         int64 // complete frames only
	TruncatedBytes int64 // tail bytes that do not form a complete frame
}

// Duration returns the exact duration implied by the complete frames.
func (i *Info) Duration() clock.Time {
	// Frames/sampleRate expressed as ticks at the sample rate.
	return clock.Time{Ticks: i.Frames, Timescale: i.SampleRate}
}

// SampleFrame is one interleaved frame: Channels samples of BytesPerSample
// bytes each, copied verbatim from the input.
type SampleFrame struct {
	Index  int64 // frame index from the start of the data chunk
	Offset int64 // raw byte offset of the first sample in the input
	Bytes  []byte
}

// Reader streams complete sample frames from a WAV file.
type Reader struct {
	info  Info
	f     *os.File
	pos   int64
	frame int64
	buf   []byte
	// completeThrough is the last byte offset that belongs to a full frame.
	completeThrough int64
}

// Open parses the RIFF/RF64 header and fmt chunk without reading audio data.
// A declared data size larger than the file yields a Truncated info with
// Frames limited to the complete prefix; that condition is recoverable and is
// also returned so callers can report it.
func Open(path, relPath string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, relPath, -1, "open wav", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fault.Wrap(fault.KindPath, relPath, -1, "stat wav", err)
	}
	info, faultErr := parseHeader(f, st.Size(), relPath)
	if info == nil {
		f.Close()
		return nil, faultErr
	}
	f.Close()
	return info, faultErr
}

func parseHeader(f io.ReadSeeker, size int64, path string) (*Info, error) {
	if size < 12 {
		return nil, fault.New(fault.KindTruncate, path, 0, "file shorter than RIFF header")
	}
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, fault.Wrap(fault.KindTruncate, path, 0, "short RIFF header", err)
	}
	rf64 := false
	switch string(hdr[0:4]) {
	case "RIFF":
	case "RF64":
		rf64 = true
	default:
		return nil, fault.New(fault.KindFormat, path, 0, "not a RIFF/RF64 file")
	}
	if string(hdr[8:12]) != "WAVE" {
		return nil, fault.New(fault.KindFormat, path, 8, "RIFF form is not WAVE")
	}
	info := &Info{Path: path, RF64: rf64}
	var ds64DataSize int64 = -1
	var off int64 = 12
	var fmtSeen, dataSeen, ds64Seen bool

	for off+8 <= size {
		ch := make([]byte, 8)
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, fault.Wrap(fault.KindFormat, path, off, "seek chunk", err)
		}
		if _, err := io.ReadFull(f, ch); err != nil {
			info := info
			return info, fault.Wrap(fault.KindTruncate, path, off, "truncated chunk header", err)
		}
		id := string(ch[0:4])
		declared := int64(binary.LittleEndian.Uint32(ch[4:8]))
		body := off + 8
		// RIFF chunks are word aligned; a missing pad byte at EOF is tolerated.
		next := body + declared + declared%2

		switch id {
		case "ds64":
			if declared < 28 || body+28 > size {
				return nil, fault.New(fault.KindCorrupt, path, off, "ds64 chunk too small")
			}
			bodyBytes := make([]byte, declared)
			if _, err := io.ReadFull(f, bodyBytes); err != nil {
				return nil, fault.Wrap(fault.KindTruncate, path, body, "truncated ds64", err)
			}
			ds64DataSize = int64(binary.LittleEndian.Uint64(bodyBytes[8:16]))
			frames := binary.LittleEndian.Uint64(bodyBytes[16:24])
			_ = frames
			ds64Seen = true
		case "fmt ":
			if declared < 16 {
				return nil, fault.New(fault.KindCorrupt, path, off, "fmt chunk smaller than 16 bytes")
			}
			if body+16 > size {
				return nil, fault.New(fault.KindTruncate, path, body, "truncated fmt chunk").Recover(false)
			}
			b := make([]byte, 16)
			if _, err := io.ReadFull(f, b); err != nil {
				return nil, fault.Wrap(fault.KindTruncate, path, body, "truncated fmt", err)
			}
			info.FormatTag = binary.LittleEndian.Uint16(b[0:2])
			info.Channels = int(binary.LittleEndian.Uint16(b[2:4]))
			info.SampleRate = binary.LittleEndian.Uint32(b[4:8])
			info.ByteRate = binary.LittleEndian.Uint32(b[8:12])
			info.BlockAlign = int(binary.LittleEndian.Uint16(b[12:14]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(b[14:16]))
			// WAVE_FORMAT_EXTENSIBLE carries the sub-format GUID after 16 bytes.
			if info.FormatTag == formatExt {
				if declared < 40 {
					return nil, fault.New(fault.KindCorrupt, path, off, "extensible fmt chunk too small")
				}
				if body+40 > size {
					return nil, fault.New(fault.KindTruncate, path, body, "truncated extensible fmt").Recover(false)
				}
				ext := make([]byte, 40)
				if _, err := io.ReadFull(f, ext); err != nil {
					return nil, fault.Wrap(fault.KindTruncate, path, body, "truncated extensible fmt", err)
				}
				info.BitsPerSample = int(binary.LittleEndian.Uint16(ext[18:20]))
				// The GUID begins with the little-endian format tag.
				info.FormatTag = binary.LittleEndian.Uint16(ext[24:26])
			}
			fmtSeen = true
			if err := validateFmt(info, path, off); err != nil {
				return nil, err
			}
		case "data":
			if !fmtSeen {
				return nil, fault.New(fault.KindCorrupt, path, off, "data chunk precedes fmt chunk")
			}
			size64 := declared
			if rf64 && ds64Seen && ds64DataSize >= 0 {
				size64 = ds64DataSize
			}
			info.DataOffset = body
			info.DataSize = size64
			info.Available = size - body
			if info.Available < 0 {
				info.Available = 0
			}
			dataSeen = true
			// Compute frames from the complete prefix.
			frames := info.Available / int64(info.BlockAlign)
			if size64 < info.Available {
				frames = size64 / int64(info.BlockAlign)
			}
			info.Frames = frames
			complete := frames * int64(info.BlockAlign)
			declaredForTrunc := size64
			if declaredForTrunc > info.Available {
				declaredForTrunc = info.Available
			}
			info.TruncatedBytes = declaredForTrunc - complete
			if size64 > info.Available {
				// The resource is shorter than declared: keep the prefix.
				trunc := fault.New(fault.KindTruncate, path, body+complete,
					fmt.Sprintf("data chunk declares %d bytes, only %d present", size64, info.Available)).
					Recover(frames > 0)
				return info, trunc
			}
			if info.TruncatedBytes > 0 {
				trunc := fault.New(fault.KindTruncate, path, body+complete,
					fmt.Sprintf("%d trailing bytes do not form a complete frame", info.TruncatedBytes)).
					Recover(true)
				return info, trunc
			}
			return info, nil
		}
		if next <= off {
			return nil, fault.New(fault.KindCorrupt, path, off, "chunk size does not advance")
		}
		off = next
	}
	if !fmtSeen {
		return nil, fault.New(fault.KindMissing, path, 0, "no fmt chunk")
	}
	if !dataSeen {
		return nil, fault.New(fault.KindMissing, path, 12, "no data chunk")
	}
	return nil, fault.New(fault.KindTruncate, path, off, "chunk headers end before data")
}

func validateFmt(info *Info, path string, off int64) error {
	if name, ok := formatNames[info.FormatTag]; ok {
		return fault.New(fault.KindUnsupported, path, off,
			fmt.Sprintf("unsupported WAV encoding: %s (0x%04x)", name, info.FormatTag)).Recover(false)
	}
	if info.FormatTag != formatPCM {
		return fault.New(fault.KindUnsupported, path, off,
			fmt.Sprintf("unsupported WAV format tag 0x%04x", info.FormatTag)).Recover(false)
	}
	if info.Channels <= 0 || info.Channels > 32 {
		return fault.New(fault.KindCorrupt, path, off,
			fmt.Sprintf("invalid channel count %d", info.Channels)).Recover(false)
	}
	if info.SampleRate == 0 {
		return fault.New(fault.KindCorrupt, path, off, "invalid zero sample rate").Recover(false)
	}
	switch info.BitsPerSample {
	case 16, 24, 32:
	default:
		return fault.New(fault.KindUnsupported, path, off,
			fmt.Sprintf("unsupported PCM bit depth %d", info.BitsPerSample)).Recover(false)
	}
	wantAlign := info.Channels * (info.BitsPerSample / 8)
	if info.BlockAlign != wantAlign {
		return fault.New(fault.KindCorrupt, path, off,
			fmt.Sprintf("block align %d inconsistent with %d channels x %d bits", info.BlockAlign, info.Channels, info.BitsPerSample)).
			Recover(false)
	}
	if info.ByteRate != info.SampleRate*uint32(info.BlockAlign) {
		return fault.New(fault.KindCorrupt, path, off,
			fmt.Sprintf("byte rate %d inconsistent with %d Hz x %d bytes", info.ByteRate, info.SampleRate, info.BlockAlign)).
			Recover(false)
	}
	info.BytesPerSample = info.BitsPerSample / 8
	return nil
}

// Reader streams complete frames. Call Next until io.EOF.
func NewReader(path, relPath string, info *Info) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, relPath, -1, "open wav", err)
	}
	r := &Reader{info: *info, f: f, pos: info.DataOffset,
		completeThrough: info.DataOffset + info.Frames*int64(info.BlockAlign),
		buf:             make([]byte, info.BlockAlign)}
	return r, nil
}

// Info returns the parsed header.
func (r *Reader) Info() Info { return r.info }

// Close releases the file handle.
func (r *Reader) Close() error { return r.f.Close() }

// Next reads one complete frame. It returns io.EOF when the complete prefix is
// exhausted; an unexpectedly short read is reported as a recoverable
// truncation at the exact byte offset.
func (r *Reader) Next() (*SampleFrame, error) {
	next := r.pos + int64(r.info.BlockAlign)
	if next > r.completeThrough {
		return nil, io.EOF
	}
	if _, err := r.f.ReadAt(r.buf, r.pos); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, fault.New(fault.KindTruncate, r.info.Path, r.pos, "short frame read").Recover(true)
		}
		return nil, fault.Wrap(fault.KindPath, r.info.Path, r.pos, "read frame", err)
	}
	out := make([]byte, r.info.BlockAlign)
	copy(out, r.buf)
	fr := &SampleFrame{Index: r.frame, Offset: r.pos, Bytes: out}
	r.pos = next
	r.frame++
	return fr, nil
}

// Sample extracts one channel's sample bytes from a frame. Channel is
// zero-based and must be below Info.Channels.
func (i *Info) Sample(fr *SampleFrame, channel int) []byte {
	if channel < 0 || channel >= i.Channels {
		return nil
	}
	start := channel * i.BytesPerSample
	return fr.Bytes[start : start+i.BytesPerSample]
}

// DecodeInt returns the signed integer value of one channel sample, sign
// extended from its true bit depth. Used only for inspection and keyframe-free
// verification; it never feeds timing.
func (i *Info) DecodeInt(fr *SampleFrame, channel int) (int64, bool) {
	s := i.Sample(fr, channel)
	switch len(s) {
	case 2:
		return int64(int16(binary.LittleEndian.Uint16(s))), true
	case 3:
		v := int64(s[0]) | int64(s[1])<<8 | int64(s[2])<<16
		if v&0x800000 != 0 {
			v |= ^int64(0xFF_FFFF)
		}
		return v, true
	case 4:
		return int64(int32(binary.LittleEndian.Uint32(s))), true
	default:
		return 0, false
	}
}
