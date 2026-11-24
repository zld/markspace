package stream

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// parsePayload decodes the boxes that drive timeline and sample mapping.
// Unknown payloads are skipped; their outer structure was already logged.
func parsePayload(boxType string, payload []byte, payloadBase int64, r *MP4Result) error {
	switch boxType {
	case "mfhd":
		if len(payload) < 8 {
			return r.fail(payload, "mfhd", payloadBase, 8)
		}
		r.MediaSeq = binary.BigEndian.Uint32(payload[4:8])
	case "tfhd":
		trackID, err := readTfhd(payload)
		if err != nil {
			return err
		}
		r.Tracks = appendTrack(r.Tracks, trackID)
	case "tfdt":
		base, err := readTfdt(payload)
		if err != nil {
			return err
		}
		r.BaseDTS = base
	case "trun":
		if err := readTrun(payload, payloadBase, r); err != nil {
			return err
		}
	case "mdhd":
		ts, err := readMdhd(payload)
		if err != nil {
			return err
		}
		if len(r.Tracks) > 0 {
			r.Tracks[len(r.Tracks)-1].Timescale = ts
		}
	}
	return nil
}

func appendTrack(tracks []TrackInfo, id uint32) []TrackInfo {
	for i := range tracks {
		if tracks[i].TrackID == id {
			return tracks
		}
	}
	return append(tracks, TrackInfo{TrackID: id})
}

// readTfhd validates the payload against the go-mp4 definition and
// extracts the track id.
func readTfhd(payload []byte) (uint32, error) {
	var tfhd mp4.Tfhd
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &tfhd, mp4.Context{}); err != nil {
		return 0, fmt.Errorf("stream: tfhd decode failed: %w", err)
	}
	return tfhd.TrackID, nil
}

func readTfdt(payload []byte) (uint64, error) {
	if len(payload) < 4 {
		return 0, io.ErrUnexpectedEOF
	}
	version := payload[0]
	switch version {
	case 0:
		if len(payload) < 8 {
			return 0, io.ErrUnexpectedEOF
		}
		return uint64(binary.BigEndian.Uint32(payload[4:8])), nil
	case 1:
		if len(payload) < 12 {
			return 0, io.ErrUnexpectedEOF
		}
		return binary.BigEndian.Uint64(payload[4:12]), nil
	default:
		return 0, fmt.Errorf("stream: unsupported tfdt version %d", version)
	}
}

// readTrun extracts samples. The moof-relative data offset is combined
// with the trun box's own segment position so each sample gains an
// absolute original byte offset.
func readTrun(payload []byte, payloadBase int64, r *MP4Result) error {
	if len(payload) < 8 {
		return io.ErrUnexpectedEOF
	}
	flags := uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
	count := binary.BigEndian.Uint32(payload[4:8])
	pos := 8
	var dataOffset int32
	if flags&0x000001 != 0 {
		if len(payload) < pos+4 {
			return io.ErrUnexpectedEOF
		}
		dataOffset = int32(binary.BigEndian.Uint32(payload[pos : pos+4]))
		pos += 4
	}
	if flags&0x000004 != 0 { // first-sample-flags
		pos += 4
	}
	// Locate the owning moof: trun payload base minus its 8-byte header
	// sits inside traf inside moof. The mdat payload position is
	// moofStart + dataOffset.
	moofStart := findTopLevelStart(r.Boxes, "moof")
	mdatPayloadBase := moofStart + int64(dataOffset)

	hasDur := flags&0x000100 != 0
	hasSize := flags&0x000200 != 0
	hasFlags := flags&0x000400 != 0
	hasCTO := flags&0x000800 != 0
	entryWidth := 0
	for _, ok := range []bool{hasDur, hasSize, hasFlags, hasCTO} {
		if ok {
			entryWidth += 4
		}
	}
	trackID := uint32(0)
	if len(r.Tracks) > 0 {
		trackID = r.Tracks[len(r.Tracks)-1].TrackID
	}
	dts := r.BaseDTS
	var sampleCursor int64
	for i := uint32(0); i < count; i++ {
		if len(payload) < pos+entryWidth {
			return io.ErrUnexpectedEOF
		}
		s := Sample{Index: len(r.Samples), TrackID: trackID, DTS: dts}
		if hasDur {
			s.Duration = binary.BigEndian.Uint32(payload[pos : pos+4])
			pos += 4
		}
		if hasSize {
			s.Size = binary.BigEndian.Uint32(payload[pos : pos+4])
			pos += 4
		}
		if hasFlags {
			flagWord := binary.BigEndian.Uint32(payload[pos : pos+4])
			// Bit 16 (sample_is_non_sync_sample) clear => keyframe.
			s.Keyframe = flagWord&0x00010000 == 0
			pos += 4
		} else {
			s.Keyframe = true
		}
		if hasCTO {
			pos += 4
		}
		s.ByteOffset = mdatPayloadBase + sampleCursor
		sampleCursor += int64(s.Size)
		dts += uint64(s.Duration)
		r.Samples = append(r.Samples, s)
	}
	return nil
}

func readMdhd(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, io.ErrUnexpectedEOF
	}
	version := payload[0]
	// v0: 4+4 creation/modification then timescale; v1: 8+8.
	tsOff := 4 + 8
	if version == 1 {
		tsOff = 4 + 16
	}
	if len(payload) < tsOff+4 {
		return 0, io.ErrUnexpectedEOF
	}
	return binary.BigEndian.Uint32(payload[tsOff : tsOff+4]), nil
}

// findTopLevelStart returns the segment offset of a depth-0 box.
func findTopLevelStart(boxes []BoxInfo, want string) int64 {
	for _, b := range boxes {
		if b.Depth == 0 && b.Type == want {
			return b.Offset
		}
	}
	return 0
}
