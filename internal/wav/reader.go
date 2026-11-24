package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"media-archiver/internal/media"
)

const (
	DefaultMaxBlockBytes int64 = 16 << 20
	TimestampChunkID           = "tms "
)

type Options struct {
	MaxBlockBytes int64
}

func DefaultOptions() Options {
	return Options{MaxBlockBytes: DefaultMaxBlockBytes}
}

// Document is a parsed RIFF/WAVE file. Recoverable tail corruption is stored
// in Issues and on the corresponding RawBlock rather than padded or hidden.
type Document struct {
	RIFFSize   uint32
	Format     media.PCMFormat
	Blocks     []media.RawBlock
	Issues     []*media.Error
	SourceSize int64
}

func (d *Document) FirstIssue() *media.Error {
	if d == nil || len(d.Issues) == 0 {
		return nil
	}
	return d.Issues[0]
}

// Read parses RIFF chunks. Fatal structural errors return a nil document;
// truncated PCM tails produce a document with explicit corruption issues.
func Read(path string, opts Options) (*Document, error) {
	if opts.MaxBlockBytes <= 0 {
		opts.MaxBlockBytes = DefaultMaxBlockBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, media.NewError(media.CodeInvalidConfig, media.StageParse, -1, "open WAV", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, media.NewError(media.CodeInternal, media.StageParse, -1, "stat WAV", err)
	}
	end := st.Size()
	if end < 12 {
		return nil, media.NewError(media.CodeBadRIFF, media.StageParse, 0, "file shorter than RIFF header", nil)
	}

	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, media.NewError(media.CodeBadRIFF, media.StageParse, 0, "read RIFF header", err)
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return nil, media.NewError(media.CodeBadRIFF, media.StageParse, 0, "missing RIFF/WAVE signature", nil)
	}
	doc := &Document{RIFFSize: binary.LittleEndian.Uint32(head[4:8]), SourceSize: end}

	var (
		haveFormat bool
		pendingTS  *struct {
			present bool
			ns      int64
		}
	)

	offset := int64(12)
	for offset+8 <= end {
		hdr := make([]byte, 8)
		if _, err := f.ReadAt(hdr, offset); err != nil {
			return nil, media.NewError(media.CodeBadRIFF, media.StageParse, offset, "read chunk header", err)
		}
		id := string(hdr[0:4])
		size := binary.LittleEndian.Uint32(hdr[4:8])
		dataOffset := offset + 8
		chunkEnd := dataOffset + int64(size)
		if chunkEnd < dataOffset {
			return nil, media.NewError(media.CodeBadRIFF, media.StageParse, offset, "chunk size overflow", nil)
		}

		switch id {
		case "fmt ":
			format, err := readFormat(f, dataOffset, int64(size), end)
			if err != nil {
				return nil, err
			}
			doc.Format = format
			haveFormat = true

		case TimestampChunkID:
			ts, err := readTimestamp(f, dataOffset, int64(size), end)
			if err != nil {
				doc.Issues = append(doc.Issues, err)
			} else {
				pendingTS = ts
			}

		case "data":
			if !haveFormat {
				return nil, media.NewError(media.CodeBadRIFF, media.StageParse, offset, "data chunk before fmt chunk", nil)
			}
			block, issue, fatal := readDataBlock(f, doc.Format, len(doc.Blocks), offset, dataOffset, size, end, opts.MaxBlockBytes, pendingTS)
			if fatal != nil {
				return nil, fatal
			}
			if issue != nil {
				doc.Issues = append(doc.Issues, issue)
			}
			doc.Blocks = append(doc.Blocks, block)
			pendingTS = nil
		}

		// RIFF chunks are two-byte aligned.
		offset = chunkEnd
		if size%2 == 1 {
			offset++
		}
		if chunkEnd > end && id == "data" {
			break
		}
	}

	if !haveFormat {
		return nil, media.NewError(media.CodeUnsupported, media.StageParse, 12, "missing fmt chunk", nil)
	}
	if len(doc.Blocks) == 0 {
		return nil, media.NewError(media.CodeBadRIFF, media.StageParse, 12, "no PCM data chunks", nil)
	}
	return doc, nil
}

func readFormat(r io.ReaderAt, offset, size, fileEnd int64) (media.PCMFormat, error) {
	if size < 16 {
		return media.PCMFormat{}, media.NewError(media.CodeUnsupported, media.StageParse, offset, "fmt chunk shorter than 16 bytes", nil)
	}
	if offset+16 > fileEnd {
		return media.PCMFormat{}, media.NewError(media.CodeTruncated, media.StageParse, offset, "truncated fmt chunk", nil)
	}
	buf := make([]byte, 16)
	if _, err := r.ReadAt(buf, offset); err != nil {
		return media.PCMFormat{}, media.NewError(media.CodeTruncated, media.StageParse, offset, "read fmt chunk", err)
	}
	f := media.PCMFormat{
		AudioFormat:   binary.LittleEndian.Uint16(buf[0:2]),
		Channels:      binary.LittleEndian.Uint16(buf[2:4]),
		SampleRate:    binary.LittleEndian.Uint32(buf[4:8]),
		ByteRate:      binary.LittleEndian.Uint32(buf[8:12]),
		BlockAlign:    binary.LittleEndian.Uint16(buf[12:14]),
		BitsPerSample: binary.LittleEndian.Uint16(buf[14:16]),
	}
	if err := f.Validate(); err != nil {
		me := media.AsError(err)
		me.Offset = offset
		return media.PCMFormat{}, err
	}
	return f, nil
}

func readTimestamp(r io.ReaderAt, offset, size, fileEnd int64) (*struct {
	present bool
	ns      int64
}, *media.Error) {
	if size < 16 || offset+16 > fileEnd {
		return nil, media.NewError(media.CodeBadRIFF, media.StageParse, offset, "truncated timestamp chunk", nil)
	}
	buf := make([]byte, 16)
	if _, err := r.ReadAt(buf, offset); err != nil {
		return nil, media.NewError(media.CodeBadRIFF, media.StageParse, offset, "read timestamp chunk", err)
	}
	flags := binary.LittleEndian.Uint32(buf[0:4])
	ns := int64(binary.LittleEndian.Uint64(buf[8:16]))
	return &struct {
		present bool
		ns      int64
	}{present: flags&1 == 1, ns: ns}, nil
}

func readDataBlock(r io.ReaderAt, format media.PCMFormat, index int, chunkOffset, dataOffset int64,
	declared uint32, fileEnd, maxBlock int64, ts *struct {
		present bool
		ns      int64
	},
) (media.RawBlock, *media.Error, error) {
	if int64(declared) > maxBlock {
		return media.RawBlock{}, nil, media.NewError(media.CodeBlockTooLarge, media.StageParse, dataOffset,
			fmt.Sprintf("data chunk %d bytes exceeds limit %d", declared, maxBlock), nil)
	}
	available := int64(declared)
	truncatedFile := dataOffset+int64(declared) > fileEnd
	if truncatedFile {
		available = fileEnd - dataOffset
		if available < 0 {
			available = 0
		}
	}
	data := make([]byte, available)
	if available > 0 {
		n, err := r.ReadAt(data, dataOffset)
		if err != nil && !errors.Is(err, io.EOF) {
			return media.RawBlock{}, nil, media.NewError(media.CodeInternal, media.StageParse, dataOffset, "read data chunk", err)
		}
		if int64(n) < available {
			data = data[:n]
			truncatedFile = true
		}
	}

	frameBytes := format.FrameByteCount()
	complete := int64(len(data) / frameBytes)
	tail := len(data) % frameBytes
	corruptOffset := dataOffset + complete*int64(frameBytes)
	block := media.RawBlock{
		Index:            index,
		ChunkOffset:      chunkOffset,
		DataOffset:       dataOffset,
		DeclaredByteSize: declared,
		Data:             data,
		CompleteFrames:   complete,
		TailBytes:        tail,
		CorruptOffset:    -1,
	}
	if ts != nil {
		block.TimestampPresent = ts.present
		block.TimestampNS = ts.ns
	}

	var issue *media.Error
	if truncatedFile || tail != 0 {
		block.Truncated = true
		block.CorruptOffset = corruptOffset
		block.CorruptionReason = "incomplete PCM sample frame at tail"
		issue = media.NewError(media.CodeTruncated, media.StageParse, corruptOffset, block.CorruptionReason, io.ErrUnexpectedEOF)
	}
	return block, issue, nil
}
