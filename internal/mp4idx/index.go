// Package mp4idx parses MP4 sample tables and sync-sample boxes via
// github.com/abema/go-mp4. It never scans past declared box sizes: every
// out-of-range chunk offset or table entry is rejected with its box type and
// byte offset, and mdat payloads are never loaded into memory.
package mp4idx

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
)

// Sample is one entry of the rebuilt sample table.
type Sample struct {
	Number      uint32       // 1-based
	Offset      int64        // absolute byte offset
	Size        uint32
	DTS         clock.Ticks  // decode time on the shared timebase
	IsSync      bool
	SyncKnown   bool         // false when sync state cannot be proven
}

// Index is the validated sample/sync index for one track.
type Index struct {
	Timescale     uint32
	Samples       []Sample
	SyncCount     int
	SyncRebuilt   bool
	Unrecoverable bool
	Warnings      []string
}

// Reader extracts exactly one video track index from an MP4 stream.
type Reader struct {
	source string
	r      io.ReadSeeker
}

// NewReader wraps a seekable, read-only MP4 stream.
func NewReader(source string, r io.ReadSeeker) *Reader {
	return &Reader{source: source, r: r}
}

type rawTables struct {
	source                            string
	timescale                         uint32
	offsetBoxType                     string
	stts                              *mp4.Stts
	stsz                              *mp4.Stsz
	stsc                              *mp4.Stsc
	chunks                            []chunk
	stss                              *mp4.Stss
	stssBoxInfo                       *mp4.BoxInfo
	anyBoxInfo                        *mp4.BoxInfo
}

type chunk struct {
	offset uint64
}

// Read walks the box structure once, collecting the sample-table boxes.
func (rd *Reader) Read() (*Index, error) {
	raw := &rawTables{source: rd.source}
	_, err := mp4.ReadBoxStructure(rd.r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd(), mp4.BoxTypeStts(), mp4.BoxTypeStsz(),
			mp4.BoxTypeStsc(), mp4.BoxTypeStco(), mp4.BoxTypeCo64(), mp4.BoxTypeStss():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, rd.boxFault(h.BoxInfo, perr)
			}
			bi := h.BoxInfo
			switch b := box.(type) {
			case *mp4.Mdhd:
				raw.timescale = b.Timescale
			case *mp4.Stts:
				raw.stts = b
			case *mp4.Stsz:
				raw.stsz = b
			case *mp4.Stsc:
				raw.stsc = b
			case *mp4.Stco:
				raw.offsetBoxType = "stco"
				for _, off := range b.ChunkOffset {
					raw.chunks = append(raw.chunks, chunk{offset: uint64(off)})
				}
			case *mp4.Co64:
				raw.offsetBoxType = "co64"
				for _, off := range b.ChunkOffset {
					raw.chunks = append(raw.chunks, chunk{offset: off})
				}
			case *mp4.Stss:
				raw.stss = b
				raw.stssBoxInfo = &bi
			}
			raw.anyBoxInfo = &bi
			return nil, nil
		}
		return h.Expand()
	})
	if err != nil {
		return nil, err
	}
	if raw.timescale == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, rd.source, "mdhd timescale missing")
	}
	if raw.stts == nil || raw.stsz == nil || raw.stsc == nil {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, rd.source,
			"sample table is incomplete: stts/stsz/stsc are required")
	}
	return rd.build(raw)
}

func (rd *Reader) boxFault(bi mp4.BoxInfo, cause error) error {
	return fault.AtOffset(fault.StageIndex, fault.KindBounds, rd.source, int64(bi.Offset),
		fmt.Sprintf("box %s payload error: %v", bi.Type.String(), cause))
}
