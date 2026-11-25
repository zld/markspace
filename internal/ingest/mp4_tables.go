package ingest

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediavault/internal/timeline"
)

// readPayload reads the full payload of a box header. A short read means the
// fixture was truncated mid-box and is reported with the exact offset.
func readPayload(r io.ReadSeeker, h *BoxHeader) ([]byte, error) {
	size := h.Size - h.HeaderSize
	if h.Size == 0 {
		end, err := r.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		size = end - h.PayloadOff
	}
	if _, err := r.Seek(h.PayloadOff, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
			Offset: h.Offset, Recoverable: false,
			Detail: fmt.Sprintf("box %s payload truncated: %v", h.Type, err), Err: err,
		}
	}
	return buf, nil
}

// unmarshalBox decodes one known box type through go-mp4's marshaller.
func unmarshalBox(r io.ReadSeeker, h *BoxHeader, dst mp4.IBox) error {
	payloadSize := uint64(0)
	if h.Size > 0 {
		payloadSize = uint64(h.Size - h.HeaderSize)
	}
	if _, err := r.Seek(h.PayloadOff, io.SeekStart); err != nil {
		return err
	}
	if _, err := mp4.Unmarshal(r, payloadSize, dst, mp4.Context{}); err != nil {
		return &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
			Offset: h.Offset, Recoverable: false,
			Detail: fmt.Sprintf("box %s decode failed: %v", h.Type, err), Err: err,
		}
	}
	return nil
}

// parseTrackTables walks one trak subtree and fills the track tables.
func parseTrackTables(r io.ReadSeeker, trak *BoxHeader, t *mp4Track) error {
	return walkBoxes(r, trak.PayloadOff, trak.Offset+trak.Size,
		map[BoxType]bool{boxMdia: true, boxMinf: true, boxStbl: true},
		func(h *BoxHeader, _ int64) error {
			switch h.Type {
			case boxMdhd:
				var mdhd mp4.Mdhd
				if err := unmarshalBox(r, h, &mdhd); err != nil {
					return err
				}
				t.timescale = mdhd.Timescale
				t.duration = mdhd.GetDuration()
			case boxHdlr:
				var hdlr mp4.Hdlr
				if err := unmarshalBox(r, h, &hdlr); err != nil {
					return err
				}
				switch string(hdlr.HandlerType[:]) {
				case "vide":
					t.kind = timeline.KindVideo
				case "soun":
					t.kind = timeline.KindAudio
				}
			case boxStts:
				var stts mp4.Stts
				if err := unmarshalBox(r, h, &stts); err != nil {
					return err
				}
				t.stts = stts.Entries
			case boxStsz:
				var stsz mp4.Stsz
				if err := unmarshalBox(r, h, &stsz); err != nil {
					return err
				}
				t.uniform = stsz.SampleSize
				t.sizes = stsz.EntrySize
			case boxStsc, boxStco, boxCo64:
				if err := parseChunkBox(r, h, t); err != nil {
					return err
				}
			}
			return nil
		})
}

// parseChunkBox decodes stsc/stco/co64 with plain big-endian reads; these
// tables are simple enough that the go-mp4 marshaller adds little, and the
// explicit bounds checks produce precise truncation offsets.
func parseChunkBox(r io.ReadSeeker, h *BoxHeader, t *mp4Track) error {
	payload, err := readPayload(r, h)
	if err != nil {
		return err
	}
	// FullBox: version(1)+flags(3)+entry_count(4) = 8 byte header.
	if len(payload) < 8 {
		return truncErr(h, "table header shorter than 8 bytes")
	}
	count := binary.BigEndian.Uint32(payload[4:8])
	body := payload[8:]
	switch h.Type {
	case boxStsc:
		if uint64(len(body)) < uint64(count)*12 {
			return truncErr(h, "stsc entries exceed payload")
		}
		for i := uint32(0); i < count; i++ {
			row := body[i*12:]
			t.stsc = append(t.stsc, mp4.StscEntry{
				FirstChunk:      binary.BigEndian.Uint32(row[0:4]),
				SamplesPerChunk: binary.BigEndian.Uint32(row[4:8]),
			})
		}
	case boxStco:
		t.offsetWide = false
		if err := fillOffsets(body, count, 4, h, t); err != nil {
			return err
		}
	case boxCo64:
		t.offsetWide = true
		if err := fillOffsets(body, count, 8, h, t); err != nil {
			return err
		}
	}
	return nil
}

func fillOffsets(body []byte, count, width uint32, h *BoxHeader, t *mp4Track) error {
	if uint64(len(body)) < uint64(count)*uint64(width) {
		return truncErr(h, "chunk offset entries exceed payload")
	}
	t.offsets = make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		row := body[i*width:]
		if width == 4 {
			t.offsets[i] = uint64(binary.BigEndian.Uint32(row[:4]))
		} else {
			t.offsets[i] = uint64(binary.BigEndian.Uint64(row[:8]))
		}
	}
	return nil
}

func truncErr(h *BoxHeader, detail string) *timeline.StageError {
	return &timeline.StageError{
		Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
		Offset: h.Offset, Recoverable: false, Detail: detail,
	}
}
