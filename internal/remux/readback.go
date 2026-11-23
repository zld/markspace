package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"mediadist/internal/codec"
	"mediadist/internal/demux"
	"mediadist/internal/merr"
)

// ReadBackSample is one sample recovered from a written MP4.
type ReadBackSample struct {
	TrackID uint32
	Index   int
	DTS     uint64
	Size    uint32
	Data    []byte
	// CanonHash is the digest of the sample's media-bearing bytes: NAL
	// payloads without length prefixes for H.264/H.265, raw OBUs for AV1.
	CanonHash string
}

// ReadBack re-parses a written MP4 and returns every sample with its canonical
// digest. It is used to prove the file on disk still carries the same media,
// rather than trusting the writer's own bookkeeping.
func ReadBack(path string) ([]ReadBackSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, merr.Wrap(merr.StageRemux, "readback_open", merr.NoSpan(0), false, err,
			"opening output for read-back")
	}
	defer f.Close()

	type trackInfo struct {
		id        uint32
		timescale uint32
		entry     mp4.BoxType
		sizes     []uint32
		offsets   []uint64
		dts       []uint64
	}
	infos := map[int]*trackInfo{}
	var order []int
	// Tracks are traversed contiguously in depth-first order, so the current
	// track is simply the last trak seen.
	cur := -1

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		if len(h.Path) == 0 {
			return nil, nil
		}
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd():
			return h.Expand()
		case mp4.BoxTypeTrak():
			infos[len(infos)] = &trackInfo{}
			cur = len(infos) - 1
			order = append(order, cur)
			return h.Expand()
		}
		if cur < 0 {
			return nil, nil
		}
		info := infos[cur]
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.id = box.(*mp4.Tkhd).TrackID
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.timescale = box.(*mp4.Mdhd).Timescale
		case mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stsz := box.(*mp4.Stsz)
			if stsz.SampleSize != 0 {
				for i := uint32(0); i < stsz.SampleCount; i++ {
					info.sizes = append(info.sizes, stsz.SampleSize)
				}
			} else {
				info.sizes = append(info.sizes, stsz.EntrySize...)
			}
		case mp4.BoxTypeStco():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, off := range box.(*mp4.Stco).ChunkOffset {
				info.offsets = append(info.offsets, uint64(off))
			}
		case mp4.BoxTypeCo64():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.offsets = append(info.offsets, box.(*mp4.Co64).ChunkOffset...)
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			var t uint64
			for _, e := range box.(*mp4.Stts).Entries {
				for i := uint32(0); i < e.SampleCount; i++ {
					info.dts = append(info.dts, t)
					t += uint64(e.SampleDelta)
				}
			}
		default:
			if isSampleEntryType(h.BoxInfo.Type) {
				info.entry = h.BoxInfo.Type
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, merr.Wrap(merr.StageRemux, "readback_parse", merr.NoSpan(0), false, err,
			"box walk failed")
	}

	// mdat payload bounds, so offsets can be validated rather than trusted.
	mdats, err := mp4.ExtractBox(f, nil, mp4.BoxPath{mp4.BoxTypeMdat()})
	if err != nil || len(mdats) == 0 {
		return nil, merr.New(merr.StageRemux, "readback_mdat", merr.NoSpan(0), false,
			"output has no mdat box")
	}
	mdat := mdats[0]
	payloadStart := mdat.Offset + mdat.HeaderSize
	payloadEnd := mdat.Offset + mdat.Size

	sort.Ints(order)
	var out []ReadBackSample
	for _, idx := range order {
		cur := infos[idx]
		if len(cur.sizes) != len(cur.dts) {
			return nil, merr.New(merr.StageRemux, "readback_table",
				merr.Span{TrackID: cur.id}, false,
				"track %d has %d sizes but %d decode times", cur.id, len(cur.sizes), len(cur.dts))
		}
		if len(cur.offsets) == 0 {
			return nil, merr.New(merr.StageRemux, "readback_chunks",
				merr.Span{TrackID: cur.id}, false, "track %d has no chunk offsets", cur.id)
		}
		if len(cur.offsets) != 1 {
			return nil, merr.New(merr.StageRemux, "readback_chunks",
				merr.Span{TrackID: cur.id}, false,
				"track %d declares %d chunks; this writer emits one", cur.id, len(cur.offsets))
		}
		pos := cur.offsets[0]
		for i, size := range cur.sizes {
			if pos < payloadStart || pos+uint64(size) > payloadEnd {
				return nil, merr.New(merr.StageRemux, "readback_range",
					merr.Span{TrackID: cur.id, Offset: int64(pos), Length: int64(size)}, false,
					"sample %d lies outside mdat", i)
			}
			if _, err := f.Seek(int64(pos), io.SeekStart); err != nil {
				return nil, merr.Wrap(merr.StageRemux, "readback_seek", merr.Span{TrackID: cur.id}, false, err,
					"seeking sample %d", i)
			}
			buf := make([]byte, size)
			if _, err := io.ReadFull(f, buf); err != nil {
				return nil, merr.Wrap(merr.StageRemux, "readback_read", merr.Span{TrackID: cur.id}, false, err,
					"reading sample %d", i)
			}
			out = append(out, ReadBackSample{
				TrackID:   cur.id,
				Index:     i,
				DTS:       cur.dts[i],
				Size:      size,
				Data:      buf,
				CanonHash: canonForEntry(cur.entry, buf),
			})
			pos += uint64(size)
		}
	}
	return out, nil
}

func isSampleEntryType(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeAvc1(), mp4.BoxTypeHvc1(), mp4.BoxTypeHev1(), mp4.BoxTypeAv01():
		return true
	}
	return false
}

// canonForEntry strips container framing to the media-bearing bytes.
func canonForEntry(entry mp4.BoxType, data []byte) string {
	switch entry {
	case mp4.BoxTypeAvc1(), mp4.BoxTypeHvc1(), mp4.BoxTypeHev1():
		canon, _ := codec.LengthPrefixedCanonical(data, codec.DefaultScanLimits())
		sum := sha256.Sum256(canon)
		return hex.EncodeToString(sum[:])
	default:
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
}

// VerifyReadBack compares re-parsed samples against the pre-write plans. It
// returns the number of samples matched and the first failure, if any.
func VerifyReadBack(path string, plans []TrackPlan) (int, int, error) {
	samples, err := ReadBack(path)
	if err != nil {
		return 0, 0, err
	}
	byTrack := map[uint32][]ReadBackSample{}
	for _, s := range samples {
		byTrack[s.TrackID] = append(byTrack[s.TrackID], s)
	}
	total, matched := 0, 0
	for _, p := range plans {
		got := byTrack[p.TrackID]
		if len(got) != len(p.Samples) {
			return matched, total, merr.New(merr.StageRemux, "readback_count",
				merr.Span{TrackID: p.TrackID}, false,
				"track %d read back %d samples, wrote %d", p.TrackID, len(got), len(p.Samples))
		}
		for i, src := range p.Samples {
			total++
			want := expectedCanon(p, src)
			if got[i].CanonHash != want {
				return matched, total, merr.New(merr.StageRemux, "readback_content",
					merr.Span{TrackID: p.TrackID, Offset: int64(i)}, false,
					"track %d sample %d canonical digest changed on disk", p.TrackID, i)
			}
			matched++
		}
	}
	return matched, total, nil
}

// expectedCanon computes the canonical digest the writer intended for a plan
// sample, independent of the layout's own recorded hashes.
func expectedCanon(p TrackPlan, s demux.Sample) string {
	stored, canon, err := payloadFor(p.Codec, s.Data)
	if err != nil {
		return ""
	}
	// payloadFor already returns the canonical digest for this sample; the
	// stored form is only checked for size elsewhere.
	_ = stored
	return canon
}
