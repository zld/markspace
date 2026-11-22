package buffer

import (
	"bytes"
	"encoding/binary"
	"io"

	"mediacore"
)

// WAVReader is the parsed, fully validated result of a WAV container.
type WAVReader struct {
	Track       mediacore.TrackID
	Rate        int
	Channels    int
	BitsPer     int
	Data        []byte
	DataOffset  int64
	TotalFrames int
}

// ParseWAV parses a RIFF/WAVE stream with strict chunk validation, bounded
// allocation and explicit errors for truncation or illegal channel layout.
// Only canonical PCM (format tag 1) is accepted.
func ParseWAV(track mediacore.TrackID, r io.Reader) (*WAVReader, error) {
	buf, err := io.ReadAll(io.LimitReader(r, mediacore.MaxBlockBytes+1))
	if err != nil {
		return nil, mediacore.WrapError(mediacore.CatCorrupt, track, "wav.read", 0, "unreadable WAV", err)
	}
	if len(buf) > mediacore.MaxBlockBytes {
		return nil, mediacore.NewError(mediacore.CatLimit, track, "wav.size", int64(len(buf)),
			"WAV exceeds MaxBlockBytes")
	}
	if len(buf) < 44 || !bytes.Equal(buf[0:4], []byte("RIFF")) ||
		!bytes.Equal(buf[8:12], []byte("WAVE")) {
		return nil, mediacore.NewError(mediacore.CatCorrupt, track, "wav.header", 0,
			"missing RIFF/WAVE signature")
	}
	if binary.LittleEndian.Uint32(buf[4:8]) != uint32(len(buf)-8) {
		return nil, mediacore.NewError(mediacore.CatCorrupt, track, "wav.riffsize", 4,
			"RIFF size does not match container length")
	}
	rd := &WAVReader{Track: track}
	pos := 12
	sawFmt, sawData := false, false
	for pos+8 <= len(buf) {
		id := string(buf[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(buf[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(buf) {
			return nil, mediacore.NewError(mediacore.CatCorrupt, track, "wav.chunk", int64(pos),
				"truncated or overlapping WAV chunk: "+id)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, mediacore.NewError(mediacore.CatCorrupt, track, "wav.fmt", int64(pos),
					"fmt chunk shorter than 16 bytes")
			}
			if tag := binary.LittleEndian.Uint16(buf[body : body+2]); tag != 1 {
				return nil, mediacore.NewError(mediacore.CatLayout, track, "wav.formattag", int64(body),
					"only uncompressed PCM (tag 1) is supported")
			}
			rd.Channels = int(binary.LittleEndian.Uint16(buf[body+2 : body+4]))
			rd.Rate = int(binary.LittleEndian.Uint32(buf[body+4 : body+8]))
			rd.BitsPer = int(binary.LittleEndian.Uint16(buf[body+14 : body+16]))
			sawFmt = true
		case "data":
			rd.Data = append([]byte(nil), buf[body:body+size]...)
			rd.DataOffset = int64(body)
			sawData = true
		}
		pos = body + size + size&1 // chunks are word-aligned
	}
	if !sawFmt || !sawData {
		return nil, mediacore.NewError(mediacore.CatEmpty, track, "wav.chunks", 0,
			"WAV missing fmt or data chunk")
	}
	if rd.Channels <= 0 || rd.Channels > mediacore.MaxChannels {
		return nil, mediacore.NewError(mediacore.CatLayout, track, "wav.channels", 0,
			"illegal WAV channel layout")
	}
	if rd.BitsPer != 16 || rd.Rate <= 0 || rd.Rate > 1<<24 {
		return nil, mediacore.NewError(mediacore.CatLayout, track, "wav.format", 0,
			"only 16-bit PCM at a positive rate is supported")
	}
	frame := rd.Channels * (rd.BitsPer / 8)
	if len(rd.Data)%frame != 0 {
		return nil, mediacore.NewError(mediacore.CatLayout, track, "wav.data", int64(rd.DataOffset),
			"data length not divisible by channel frame size")
	}
	rd.TotalFrames = len(rd.Data) / frame
	return rd, nil
}

// PCMBlock is one fixed-size split of the WAV payload.
type PCMBlock struct {
	Seq     uint64
	Time    int64
	Samples int
	PCM     []byte
}

// SplitPCM divides validated WAV data into deterministic fixed-size blocks.
// startNS is the integer-nanosecond origin of the first block on the master
// clock. The final partial block is returned with its true sample count.
func SplitPCM(rd *WAVReader, blockFrames int, startNS int64) ([]PCMBlock, error) {
	if blockFrames <= 0 || rd.Channels <= 0 {
		return nil, mediacore.NewError(mediacore.CatLayout, rd.Track, "pcm.split", 0,
			"block size and channel count must be positive")
	}
	frame := rd.Channels * (rd.BitsPer / 8)
	n := rd.TotalFrames
	blocks := make([]PCMBlock, 0, (n+blockFrames-1)/blockFrames)
	for off, seq := 0, uint64(0); off < n; seq++ {
		frames := blockFrames
		if off+frames > n {
			frames = n - off
		}
		blocks = append(blocks, PCMBlock{
			Seq:     seq,
			Time:    startNS + int64(off)*mediacore.Second/int64(rd.Rate),
			Samples: frames,
			PCM:     append([]byte(nil), rd.Data[off*frame:(off+frames)*frame]...),
		})
		off += frames
	}
	return blocks, nil
}

// WriteWAV serializes PCM blocks back into a canonical RIFF/WAVE stream,
// preserving sample order. All blocks must share the block's own layout.
func WriteWAV(rate, channels, bitsPer int, data []byte) ([]byte, error) {
	if channels <= 0 || channels > mediacore.MaxChannels {
		return nil, mediacore.NewError(mediacore.CatLayout, "", "wav.write.channels", 0,
			"illegal channel layout on write")
	}
	if len(data) > mediacore.MaxBlockBytes {
		return nil, mediacore.NewError(mediacore.CatLimit, "", "wav.write.size", int64(len(data)),
			"WAV payload exceeds MaxBlockBytes")
	}
	var b bytes.Buffer
	b.Grow(44 + len(data))
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*channels*bitsPer/8))
	binary.Write(&b, binary.LittleEndian, uint16(channels*bitsPer/8))
	binary.Write(&b, binary.LittleEndian, uint16(bitsPer))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes(), nil
}
