package demux

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

// DemuxMP4 parses MP4/MOV metadata with go-mp4 and preserves every sample's
// compressed bytes, timestamps, duration and original payload offset.
//
// A partially usable document is returned together with a non-nil structured
// error when a box is fatally out of bounds. Corrupt/truncated samples are
// isolated in Document.Issues and omitted from the valid sample table.
func DemuxMP4(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindUnrecoverable, err)
	}
	doc := &Document{Path: path, Format: "mp4"}
	if len(data) >= 12 && string(data[8:12]) == "qt  " {
		doc.Format = "mov"
	}

	boundsIssues := scanBoxBounds(path, data)
	doc.Issues = append(doc.Issues, boundsIssues...)

	info, perr := gomp4.Probe(bytes.NewReader(data))
	if len(boundsIssues) > 0 {
		return doc, boundsIssues[0]
	}
	if perr != nil {
		fe := fault.At(fault.KindBoxBounds, path, firstIssueOffset(boundsIssues),
			"go-mp4 probe failed: %v", perr)
		fe.Recoverable = false
		return doc, fe
	}

	for _, tt := range info.Tracks {
		track := buildTrack(path, data, tt, doc)
		doc.Tracks = append(doc.Tracks, track)
	}
	return doc, nil
}

func buildTrack(path string, data []byte, tt *gomp4.Track, doc *Document) *Track {
	tb := clock.TickRate(tt.Timescale)
	t := &Track{
		ID: tt.TrackID, Kind: kindFromProbe(tt), Timebase: tb, Duration: int64(tt.Duration),
	}
	t.Codec = CodecParams{Codec: codecName(tt.Codec), FourCC: codecName(tt.Codec)}
	if tt.AVC != nil {
		t.Codec.Width = tt.AVC.Width
		t.Codec.Height = tt.AVC.Height
	}
	if tt.MP4A != nil {
		t.Codec.Channels = tt.MP4A.ChannelCount
		t.Codec.SampleRate = tt.Timescale
	}

	sampleIndex := 0
	var dts int64
	for _, chunk := range tt.Chunks {
		offset := int64(chunk.DataOffset)
		for j := uint32(0); j < chunk.SamplesPerChunk && sampleIndex < len(tt.Samples); j++ {
			meta := tt.Samples[sampleIndex]
			size := int(meta.Size)
			s := &Sample{
				Index: sampleIndex, TrackID: tt.TrackID, DTS: dts,
				PTS: dts + meta.CompositionTimeOffset, Duration: int64(meta.TimeDelta),
				Offset: offset, Size: size,
			}
			end := offset + int64(size)
			switch {
			case size == 0:
				issue := fault.Track(fault.KindTruncated, path, tt.TrackID, offset,
					io.ErrUnexpectedEOF, true)
				doc.Issues = append(doc.Issues, issue)
			case offset < 0 || offset >= int64(len(data)) || end > int64(len(data)):
				issue := fault.Track(fault.KindTruncated, path, tt.TrackID, offset,
					io.ErrUnexpectedEOF, true)
				doc.Issues = append(doc.Issues, issue)
			default:
				payload := data[offset:end]
				if badEnvelope(payload, tt.TrackID, sampleIndex) {
					issue := fault.Track(fault.KindCorrupt, path, tt.TrackID, offset,
						errors.New("synthetic payload CRC mismatch"), true)
					doc.Issues = append(doc.Issues, issue)
				} else {
					s.Payload = append([]byte(nil), payload...)
					s.Hash = HashPayload(s.Payload)
					t.Samples = append(t.Samples, s)
				}
			}
			dts += int64(meta.TimeDelta)
			offset += int64(size)
			sampleIndex++
		}
	}
	return t
}

func scanBoxBounds(path string, data []byte) []*fault.Error {
	var issues []*fault.Error
	r := bytes.NewReader(data)
	for {
		off, _ := r.Seek(0, io.SeekCurrent)
		bi, err := gomp4.ReadBoxInfo(r)
		if err == io.EOF {
			return issues
		}
		if err != nil {
			issues = append(issues, fault.At(fault.KindBoxBounds, path, off, "invalid box header: %v", err))
			return issues
		}
		if bi.Size < bi.HeaderSize || bi.Offset+bi.Size > uint64(len(data)) {
			fe := fault.At(fault.KindBoxBounds, path, int64(bi.Offset),
				"box %s declares size %d beyond %d-byte input", bi.Type, bi.Size, len(data))
			fe.Recoverable = false
			issues = append(issues, fe)
			return issues
		}
		if _, err := bi.SeekToEnd(r); err != nil {
			issues = append(issues, fault.At(fault.KindBoxBounds, path, int64(bi.Offset), "seek box end: %v", err))
			return issues
		}
	}
}

func badEnvelope(b []byte, trackID uint32, index int) bool {
	if len(b) < 16 || string(b[:4]) != "MDPK" {
		return false // ordinary compressed payload; no synthetic checksum envelope
	}
	if binary.BigEndian.Uint16(b[4:6]) != uint16(trackID) ||
		binary.BigEndian.Uint16(b[6:8]) != uint16(index) {
		return true
	}
	want := binary.BigEndian.Uint32(b[len(b)-4:])
	return crc32.ChecksumIEEE(b[:len(b)-4]) != want
}

func kindFromProbe(t *gomp4.Track) string {
	if t.Codec == gomp4.CodecMP4A || (t.MP4A != nil) {
		return "audio"
	}
	return "video"
}
func codecName(c gomp4.Codec) string {
	switch c {
	case gomp4.CodecAVC1:
		return "avc1"
	case gomp4.CodecMP4A:
		return "mp4a"
	default:
		return "unknown"
	}
}
func firstIssueOffset(in []*fault.Error) int64 {
	if len(in) > 0 {
		return in[0].Offset
	}
	return -1
}
