// Package wav streams uncompressed PCM WAV files. It validates the RIFF length,
// every chunk boundary and frame alignment, and reports failures with a stable
// code, a stage and a byte offset. The source is opened read-only.
package wav

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"

	"example.com/mediavault/internal/clock"
	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
)

// MaxFileSize is the workspace contract for a single source file.
const MaxFileSize int64 = 2 << 30

const (
	// MaxChunkFrames bounds a derived block to 1 MiB of samples.
	MaxBlockBytes = 1 << 20
	metaBufSize   = 32 << 10
)

// ChunkInfo describes one RIFF chunk encountered during the scan.
type ChunkInfo struct {
	ID      string `json:"id"`
	Offset  int64  `json:"offset"`
	Size    int64  `json:"size"`
	Payload int64  `json:"payload"`
}

// Info is the validated description of a WAV file. It is sufficient to derive
// every block without rescanning.
type Info struct {
	Format          model.WAVFormat `json:"format"`
	RIFFSize        int64           `json:"riff_size"`
	FileSize        int64           `json:"file_size"`
	DataOffset      int64           `json:"data_offset"`
	DataSize        int64           `json:"data_size"`
	Frames          int64           `json:"frames"`
	ContainerDigest string          `json:"container_digest"`
	SourceDigest    string          `json:"source_digest"`
	Chunks          []ChunkInfo     `json:"chunks"`
}

// ScanFile opens path read-only and validates it.
func ScanFile(path string) (*os.File, *Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "open %s", path)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "stat %s", path)
	}
	info, err := Scan(f, st.Size())
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// Scan validates the RIFF envelope and all chunk boundaries.
func Scan(r io.ReaderAt, size int64) (*Info, error) {
	if size > MaxFileSize {
		return nil, fault.New(fault.CodeLimit, fault.StageScanned, -1, "file size %d exceeds %d", size, MaxFileSize)
	}
	if size < 12 {
		return nil, fault.New(fault.CodeRIFFLength, fault.StageScanned, 0, "file of %d bytes is shorter than a RIFF header", size)
	}
	hdr := make([]byte, 12)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageScanned, 0, err, "read RIFF header")
	}
	if string(hdr[0:4]) != "RIFF" {
		return nil, fault.New(fault.CodeRIFFLength, fault.StageScanned, 0, "missing RIFF magic %q", hdr[0:4])
	}
	riffSize := int64(binary.LittleEndian.Uint32(hdr[4:8]))
	if string(hdr[8:12]) != "WAVE" {
		return nil, fault.New(fault.CodeRIFFLength, fault.StageScanned, 8, "missing WAVE form %q", hdr[8:12])
	}
	declaredEnd := 8 + riffSize
	if declaredEnd != size {
		return nil, fault.New(fault.CodeRIFFLength, fault.StageScanned, 4,
			"RIFF declares end %d but file is %d bytes", declaredEnd, size)
	}

	h := sha256.New()
	writeFramed(h, hdr)

	info := &Info{RIFFSize: riffSize, FileSize: size}
	var (
		haveFmt  bool
		haveData bool
		off      = int64(12)
		buf      = make([]byte, metaBufSize)
	)
	for off < declaredEnd {
		if declaredEnd-off < 8 {
			return nil, fault.New(fault.CodeChunkBoundary, fault.StageScanned, off,
				"%d trailing bytes cannot hold a chunk header", declaredEnd-off)
		}
		var ch [8]byte
		if _, err := r.ReadAt(ch[:], off); err != nil {
			return nil, fault.Wrap(fault.CodeIO, fault.StageScanned, off, err, "read chunk header")
		}
		id := string(ch[0:4])
		csize := int64(binary.LittleEndian.Uint32(ch[4:8]))
		payload := off + 8
		payloadEnd := payload + csize
		if csize < 0 || payloadEnd > declaredEnd {
			return nil, fault.New(fault.CodeChunkBoundary, fault.StageScanned, off,
				"chunk %q of %d bytes runs past RIFF end %d", id, csize, declaredEnd)
		}
		pad := csize & 1
		next := payloadEnd + pad
		if next > declaredEnd {
			return nil, fault.New(fault.CodeChunkBoundary, fault.StageScanned, payloadEnd,
				"chunk %q is missing its pad byte", id)
		}
		writeFramed(h, ch[:])
		info.Chunks = append(info.Chunks, ChunkInfo{ID: id, Offset: off, Size: csize, Payload: payload})

		switch id {
		case "fmt ":
			if haveFmt {
				return nil, fault.New(fault.CodeFormat, fault.StageScanned, off, "duplicate fmt chunk")
			}
			format, err := parseFmt(r, payload, csize)
			if err != nil {
				return nil, err
			}
			if err := format.Validate(); err != nil {
				return nil, fault.Wrap(fault.CodeFormat, fault.StageScanned, payload, err, "unsupported WAV format")
			}
			info.Format = format
			haveFmt = true
			if err := hashPayload(r, h, payload, csize, buf); err != nil {
				return nil, err
			}
		case "data":
			if haveData {
				return nil, fault.New(fault.CodeChunkBoundary, fault.StageScanned, off, "duplicate data chunk")
			}
			if !haveFmt {
				return nil, fault.New(fault.CodeFormat, fault.StageScanned, off, "data chunk before fmt chunk")
			}
			if csize%int64(info.Format.FrameBytes()) != 0 {
				return nil, fault.New(fault.CodeFrameAlign, fault.StageScanned, payload,
					"data size %d is not a multiple of frame size %d", csize, info.Format.FrameBytes())
			}
			info.DataOffset = payload
			info.DataSize = csize
			info.Frames = csize / int64(info.Format.FrameBytes())
			haveData = true
		default:
			if err := hashPayload(r, h, payload, csize, buf); err != nil {
				return nil, err
			}
		}
		off = next
	}
	if !haveFmt {
		return nil, fault.New(fault.CodeFormat, fault.StageScanned, 12, "missing fmt chunk")
	}
	if !haveData {
		return nil, fault.New(fault.CodeChunkBoundary, fault.StageScanned, declaredEnd, "missing data chunk")
	}
	info.ContainerDigest = hex.EncodeToString(h.Sum(nil))
	src, err := hashStream(r, size)
	if err != nil {
		return nil, err
	}
	info.SourceDigest = src
	return info, nil
}

func parseFmt(r io.ReaderAt, off, size int64) (model.WAVFormat, error) {
	if size < 16 {
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off,
			"fmt chunk of %d bytes is shorter than 16", size)
	}
	b := make([]byte, 16)
	if _, err := r.ReadAt(b, off); err != nil {
		return model.WAVFormat{}, fault.Wrap(fault.CodeIO, fault.StageScanned, off, err, "read fmt chunk")
	}
	audioFormat := binary.LittleEndian.Uint16(b[0:2])
	channels := int(binary.LittleEndian.Uint16(b[2:4]))
	sampleRate := int64(binary.LittleEndian.Uint32(b[4:8]))
	byteRate := int64(binary.LittleEndian.Uint32(b[8:12]))
	blockAlign := int(binary.LittleEndian.Uint16(b[12:14]))
	bits := int(binary.LittleEndian.Uint16(b[14:16]))
	if audioFormat != 1 {
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off,
			"audio format %d is not uncompressed PCM", audioFormat)
	}
	if channels < 1 || channels > 8 {
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off+2,
			"channel count %d outside 1-8", channels)
	}
	switch bits {
	case 16, 24, 32:
	default:
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off+14,
			"bits per sample %d outside 16/24/32", bits)
	}
	frameBytes := channels * bits / 8
	if blockAlign != frameBytes {
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off+12,
			"block align %d does not match %d channels x %d bits", blockAlign, channels, bits)
	}
	if byteRate != sampleRate*int64(blockAlign) {
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off+8,
			"byte rate %d does not match %d Hz x %d bytes", byteRate, sampleRate, blockAlign)
	}
	if sampleRate < 8000 || sampleRate > 192000 {
		return model.WAVFormat{}, fault.New(fault.CodeFormat, fault.StageScanned, off+4,
			"sample rate %d outside 8-192 kHz", sampleRate)
	}
	return model.WAVFormat{
		SampleRate:    clock.NewRate(sampleRate, 1),
		Channels:      channels,
		BitsPerSample: bits,
	}, nil
}

// hashStream digests the entire source file in bounded chunks.
func hashStream(r io.ReaderAt, size int64) (string, error) {
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for off := int64(0); off < size; {
		n := int64(len(buf))
		if size-off < n {
			n = size - off
		}
		if _, err := r.ReadAt(buf[:n], off); err != nil && err != io.EOF {
			return "", fault.Wrap(fault.CodeIO, fault.StageScanned, off, err, "hash source")
		}
		h.Write(buf[:n])
		off += n
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashPayload(r io.ReaderAt, h io.Writer, off, size int64, buf []byte) error {
	var folded [8]byte
	binary.BigEndian.PutUint64(folded[:], uint64(size))
	if _, err := h.Write(folded[:]); err != nil {
		return err
	}
	for read := int64(0); read < size; {
		n := int64(len(buf))
		if size-read < n {
			n = size - read
		}
		if _, err := r.ReadAt(buf[:n], off+read); err != nil && err != io.EOF {
			return fault.Wrap(fault.CodeIO, fault.StageScanned, off+read, err, "read chunk payload")
		}
		if _, err := h.Write(buf[:n]); err != nil {
			return err
		}
		read += n
	}
	return nil
}

func writeFramed(h io.Writer, p []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(p)))
	h.Write(n[:])
	h.Write(p)
}

// Reader reads frame-aligned PCM blocks from an already scanned file with a
// bounded buffer. It is safe to resume at any frame offset.
type Reader struct {
	r      io.ReaderAt
	info   *Info
	frame  int
	buf    []byte
	cursor int64
}

// NewReader creates a sequential block reader over the data chunk.
func NewReader(r io.ReaderAt, info *Info) (*Reader, error) {
	if info == nil || info.Format.FrameBytes() <= 0 {
		return nil, fault.New(fault.CodeFormat, fault.StageScanned, -1, "reader needs a validated format")
	}
	return &Reader{r: r, info: info, frame: info.Format.FrameBytes()}, nil
}

// Frames returns the total frame count.
func (r *Reader) Frames() int64 { return r.info.Frames }

// SeekFrames positions the sequential cursor at an absolute frame index.
func (r *Reader) SeekFrames(frame int64) error {
	if frame < 0 || frame > r.info.Frames {
		return fault.New(fault.CodeArgs, fault.StageScanned, -1, "seek frame %d outside 0..%d", frame, r.info.Frames)
	}
	r.cursor = frame
	return nil
}

// Next returns the next at most maxFrames frames. io.EOF signals the end.
func (r *Reader) Next(maxFrames int64) ([]byte, int64, error) {
	if r.cursor >= r.info.Frames {
		return nil, 0, io.EOF
	}
	n := r.info.Frames - r.cursor
	if n > maxFrames {
		n = maxFrames
	}
	if n*int64(r.frame) > MaxBlockBytes {
		n = MaxBlockBytes / int64(r.frame)
	}
	pcm, err := r.ReadFrames(r.cursor, n)
	if err != nil {
		return nil, 0, err
	}
	r.cursor += n
	return pcm, n, nil
}

// ReadFrames reads an absolute frame range into one bounded buffer.
func (r *Reader) ReadFrames(frame, count int64) ([]byte, error) {
	if frame < 0 || count < 0 || frame+count > r.info.Frames {
		return nil, fault.New(fault.CodeArgs, fault.StageScanned, -1,
			"frame range %d+%d outside 0..%d", frame, count, r.info.Frames)
	}
	if count*int64(r.frame) > MaxBlockBytes {
		return nil, fault.New(fault.CodeLimit, fault.StageScanned, -1,
			"block of %d frames exceeds %d bytes", count, MaxBlockBytes)
	}
	need := count * int64(r.frame)
	if int64(cap(r.buf)) < need {
		r.buf = make([]byte, need)
	}
	buf := r.buf[:need]
	off := r.info.DataOffset + frame*int64(r.frame)
	if need > 0 {
		if _, err := r.r.ReadAt(buf, off); err != nil {
			return nil, fault.Wrap(fault.CodeIO, fault.StageScanned, off, err, "read PCM frames %d+%d", frame, count)
		}
	}
	return buf, nil
}
