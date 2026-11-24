// Package mp4 implements the versioned MP4/MOV container parser (major 1)
// on top of github.com/abema/go-mp4. The box tree is walked streaming;
// only the sample table is kept in memory, sample payloads stay in the
// source file. Truncation, unknown brands, unsupported tracks and
// timestamp anomalies are reported with byte offsets.
package mp4

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"mediacheck/internal/adapter"
	"mediacheck/internal/clock"
	"mediacheck/internal/fault"
)

// MajorForBrand maps an ftyp major brand to a registered parser major.
func MajorForBrand(brand [4]byte) (int, bool) {
	switch string(brand[:]) {
	case "isom", "iso2", "iso5", "iso6", "mp41", "mp42", "avc1", "qt  ", "M4V ":
		return 1, true
	}
	return 0, false
}

// ParserV1 parses baseline MP4/MOV containers (parser major version 1).
type ParserV1 struct{}

func (ParserV1) Major() int { return 1 }

type trakBuilder struct {
	id        uint32
	kind      adapter.Kind
	skip      bool
	timescale uint32
	stts      *gomp4.Stts
	ctts      *gomp4.Ctts
	cttsOff   int64
	stsz      *gomp4.Stsz
	stco      []uint64
	stsc      *gomp4.Stsc
	stss      map[uint32]bool
	hasStss   bool
}

// Parse walks the box tree of path and builds the media model.
func (ParserV1) Parse(ctx context.Context, path string, fl *fault.List) (*adapter.Media, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	media := &adapter.Media{Path: path, Source: f, Close: f.Close}

	var traks []*trakBuilder
	var cur *trakBuilder
	var lastEnd int64
	_, walkErr := gomp4.ReadBoxStructure(f, func(h *gomp4.ReadHandle) (interface{}, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bi := h.BoxInfo
		lastEnd = int64(bi.Offset + bi.Size)
		switch len(h.Path) {
		case 1:
			switch bi.Type {
			case gomp4.BoxTypeFtyp():
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				brand := box.(*gomp4.Ftyp).MajorBrand
				if _, ok := MajorForBrand(brand); !ok {
					fl.Add(fault.ErrUnknownMajor, int64(bi.Offset)+8,
						"unsupported major brand %q", brand)
					return nil, fmt.Errorf("mp4: unknown major brand %q", brand)
				}
			case gomp4.BoxTypeMoov():
				return h.Expand()
			}
		case 2:
			if bi.Type == gomp4.BoxTypeTrak() {
				cur = &trakBuilder{}
				traks = append(traks, cur)
				return h.Expand()
			}
		case 3:
			switch bi.Type {
			case gomp4.BoxTypeTkhd():
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				cur.id = box.(*gomp4.Tkhd).TrackID
			case gomp4.BoxTypeMdia():
				return h.Expand()
			}
		case 4:
			switch bi.Type {
			case gomp4.BoxTypeMdhd():
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				cur.timescale = box.(*gomp4.Mdhd).Timescale
			case gomp4.BoxTypeHdlr():
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				ht := box.(*gomp4.Hdlr).HandlerType
				switch string(ht[:]) {
				case "vide":
					cur.kind = adapter.KindVideo
				case "soun":
					cur.kind = adapter.KindAudio
				default:
					cur.skip = true
					fl.Add(fault.ErrUnsupportedTrack, int64(bi.Offset),
						"track %d handler %q not supported", cur.id, ht)
				}
			case gomp4.BoxTypeMinf():
				return h.Expand()
			}
		case 5:
			if bi.Type == gomp4.BoxTypeStbl() {
				return h.Expand()
			}
		case 6:
			if err := readSampleTableBox(h, cur); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if walkErr != nil {
		f.Close()
		if errors.Is(walkErr, io.ErrUnexpectedEOF) || errors.Is(walkErr, io.EOF) {
			fl.Add(fault.ErrTruncated, lastEnd,
				"container ends unexpectedly at byte %d", lastEnd)
			return nil, fmt.Errorf("mp4: truncated container at offset %d", lastEnd)
		}
		return nil, walkErr
	}
	if err := buildTracks(media, traks, fl); err != nil {
		f.Close()
		return nil, err
	}
	return media, nil
}

func readSampleTableBox(h *gomp4.ReadHandle, cur *trakBuilder) error {
	if cur == nil {
		return nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		return err
	}
	switch b := box.(type) {
	case *gomp4.Stts:
		cur.stts = b
	case *gomp4.Ctts:
		cur.ctts = b
		cur.cttsOff = int64(h.BoxInfo.Offset)
	case *gomp4.Stsz:
		cur.stsz = b
	case *gomp4.Stco:
		for _, o := range b.ChunkOffset {
			cur.stco = append(cur.stco, uint64(o))
		}
	case *gomp4.Co64:
		cur.stco = append(cur.stco, b.ChunkOffset...)
	case *gomp4.Stsc:
		cur.stsc = b
	case *gomp4.Stss:
		cur.hasStss = true
		cur.stss = make(map[uint32]bool, len(b.SampleNumber))
		for _, n := range b.SampleNumber {
			cur.stss[n] = true
		}
	}
	return nil
}

// buildTracks expands the per-trak sample tables into the media model.
func buildTracks(media *adapter.Media, traks []*trakBuilder, fl *fault.List) error {
	for _, tb := range traks {
		if tb.skip {
			continue
		}
		if tb.stts == nil || tb.stsz == nil || tb.stsc == nil || len(tb.stco) == 0 {
			return fmt.Errorf("mp4: track %d misses required sample table boxes", tb.id)
		}
		n := int(tb.stsz.SampleCount)
		sizes := make([]uint32, n)
		for i := range sizes {
			if tb.stsz.SampleSize != 0 {
				sizes[i] = tb.stsz.SampleSize
			} else if i < len(tb.stsz.EntrySize) {
				sizes[i] = tb.stsz.EntrySize[i]
			}
		}
		// DTS walk from stts.
		dts := make([]int64, n)
		dur := make([]int64, n)
		var acc int64
		idx := 0
		for _, e := range tb.stts.Entries {
			for k := uint32(0); k < e.SampleCount && idx < n; k++ {
				dts[idx] = acc
				dur[idx] = int64(e.SampleDelta)
				acc += int64(e.SampleDelta)
				idx++
			}
		}
		// CTS from ctts (version 1 allows negative offsets).
		cts := make([]int64, n)
		copy(cts, dts)
		if tb.ctts != nil {
			idx = 0
			for ei, e := range tb.ctts.Entries {
				off := tb.ctts.GetSampleOffset(ei)
				for k := uint32(0); k < e.SampleCount && idx < n; k++ {
					cts[idx] = dts[idx] + off
					idx++
				}
			}
		}
		// Sample byte offsets from stsc + stco.
		offsets := sampleOffsets(tb.stsc.Entries, tb.stco, sizes)

		ts := clock.Timescale(tb.timescale)
		tracker := clock.NewTracker(fmt.Sprintf("trak-%d", tb.id), fl)
		tr := &adapter.Track{ID: tb.id, Kind: tb.kind, Timescale: ts}
		for i := 0; i < n; i++ {
			tracker.Check(dts[i], int64(tb.stts.EntryCount))
			if cts[i] < 0 {
				fl.Add(fault.ErrNegativeStart, tb.cttsOff,
					"track %d sample %d has negative CTS %d", tb.id, i, cts[i])
			}
			key := !tb.hasStss || tb.stss[uint32(i+1)]
			tr.Samples = append(tr.Samples, adapter.Sample{
				DTS: dts[i], CTS: cts[i], Duration: dur[i],
				Size: sizes[i], Keyframe: key, Offset: offsets[i],
			})
		}
		media.Tracks = append(media.Tracks, tr)
		if d := ts.ToNanos(tr.DurationTicks()); d > media.DurationNanos {
			media.DurationNanos = d
		}
	}
	return nil
}

// sampleOffsets maps samples to byte offsets via the stsc/stco tables.
func sampleOffsets(stsc []gomp4.StscEntry, stco []uint64, sizes []uint32) []uint64 {
	offsets := make([]uint64, len(sizes))
	sample := 0
	chunk := uint32(1)
	for i, e := range stsc {
		next := uint32(len(stco) + 1)
		if i+1 < len(stsc) {
			next = stsc[i+1].FirstChunk
		}
		for ; chunk < next && chunk <= uint32(len(stco)); chunk++ {
			off := stco[chunk-1]
			for j := uint32(0); j < e.SamplesPerChunk && sample < len(sizes); j++ {
				offsets[sample] = off
				off += uint64(sizes[sample])
				sample++
			}
		}
	}
	return offsets
}
