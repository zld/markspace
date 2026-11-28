package scan

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"avpipe/internal/model"
	"avpipe/internal/mp4box"
	"github.com/pion/rtp"
)

// MaxTrackSamples bounds the in-memory index of one track. Inputs past the
// limit are rejected rather than allowed to grow without bound; samples are
// still streamed one packet at a time up to that point.
const MaxTrackSamples = 100_000

// IndexFile is the on-disk index name inside the state directory.
const IndexFile = "index.json"

// TrackIndex is the scanned timeline of one track. A structural defect (for
// example a truncated tail) is recorded here instead of aborting the case so
// the other tracks keep scanning.
type TrackIndex struct {
	Track       int                `json:"track"`
	Name        string             `json:"name"`
	ClockRate   uint32             `json:"clock_rate"`
	Samples     []model.SampleMeta `json:"samples"`
	FileDigest  string             `json:"file_digest"`
	FileSize    int64              `json:"file_size"`
	Truncated   bool               `json:"truncated"`
	TruncOffset int64              `json:"trunc_offset,omitempty"`
	TruncSeq    int                `json:"trunc_seq,omitempty"`
	// Defect is the structured, recoverable per-track error, if any.
	Defect *model.Error `json:"defect,omitempty"`
}

// Index is the complete, digest-anchored scan result for one fixture case.
type Index struct {
	CaseName  string           `json:"case_name"`
	Digest    string           `json:"digest"`
	Complete  bool             `json:"complete"`
	Mvhd      mp4box.MvhdTime  `json:"mvhd"`
	Desc      model.CaseDesc   `json:"desc"`
	Tracks    []TrackIndex     `json:"tracks"`
	FileSizes map[string]int64 `json:"file_sizes"`
}

// Scan reads the MP4 descriptor, digests every input and streams each RTP
// track into a bounded index. A per-track defect never fails the whole scan.
func Scan(dir string) (*Index, error) {
	mp4Path := filepath.Join(dir, model.IndexMP4)
	f, err := os.Open(mp4Path)
	if err != nil {
		return nil, model.NewError(model.CodeIO, false, "open mp4: %v", err)
	}
	desc, mv, err := mp4box.Read(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	digest, sizes, err := CaseDigest(dir, desc)
	if err != nil {
		return nil, err
	}
	idx := &Index{
		CaseName: desc.Name, Digest: digest, Mvhd: mv, Desc: desc,
		FileSizes: sizes,
	}
	for _, td := range desc.Tracks {
		idx.Tracks = append(idx.Tracks, scanTrack(dir, td))
	}
	idx.Complete = true
	return idx, nil
}

func scanTrack(dir string, td model.TrackDesc) TrackIndex {
	ti := TrackIndex{Track: td.ID, Name: td.Name, ClockRate: td.ClockRate}
	path := filepath.Join(dir, model.RTPPath(td.ID))
	digest, size, err := FileDigest(path)
	ti.FileDigest, ti.FileSize = digest, size
	if err != nil {
		if me, ok := model.AsError(err); ok {
			ti.Defect = me.AtTrack(td.ID, 0)
		} else {
			ti.Defect = model.NewError(model.CodeIO, false, "%v", err).AtTrack(td.ID, 0)
		}
		return ti
	}

	f, err := os.Open(path)
	if err != nil {
		ti.Defect = model.NewError(model.CodeIO, false, "open rtp: %v", err).AtTrack(td.ID, 0)
		return ti
	}
	defer f.Close()

	var offset int64
	for seq := 0; ; seq++ {
		if seq >= MaxTrackSamples {
			ti.Defect = model.NewError(model.CodeOffsetOutOfRange, false,
				"track exceeds %d samples", MaxTrackSamples).AtTrack(td.ID, offset)
			return ti
		}
		frameStart := offset
		hdr := make([]byte, 4)
		n, err := io.ReadFull(f, hdr)
		if err == io.EOF && n == 0 {
			break // clean end of stream
		}
		if err != nil {
			ti.markTrunc(td.ID, frameStart, seq, "length header")
			return ti
		}
		offset += 4
		frameLen := int64(binary.BigEndian.Uint32(hdr))
		if frameLen <= 0 || offset+frameLen > size {
			ti.markTrunc(td.ID, frameStart, seq, "declared length out of range")
			return ti
		}
		raw := make([]byte, frameLen)
		if _, err := io.ReadFull(f, raw); err != nil {
			ti.markTrunc(td.ID, frameStart, seq, "packet payload")
			return ti
		}
		offset += frameLen

		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			ti.markTrunc(td.ID, frameStart, seq, "rtp unmarshal: "+err.Error())
			return ti
		}
		rawNS := model.TicksToNS(pkt.Timestamp, td.ClockRate)
		ti.Samples = append(ti.Samples, model.SampleMeta{
			Track:       td.ID,
			Seq:         seq,
			RawTS:       pkt.Timestamp,
			Clock:       model.SourceClock{ClockRate: td.ClockRate, SourceID: pkt.SSRC},
			RawTimeNS:   rawNS,
			FixedTimeNS: rawNS,
			InputOffset: frameStart,
			Size:        len(raw),
		})
	}
	return ti
}

func (ti *TrackIndex) markTrunc(track int, offset int64, seq int, detail string) {
	ti.Truncated = true
	ti.TruncOffset, ti.TruncSeq = offset, seq
	ti.Defect = model.NewError(model.CodeTruncated, true,
		"truncated tail block at %s", detail).AtTrack(track, offset)
}

// Save writes the index atomically (temp file + rename) with its completion
// marker so an interrupted write can never leave a reusable partial index.
func (idx *Index) Save(path string) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return model.NewError(model.CodeIO, false, "marshal index: %v", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return model.NewError(model.CodeIO, false, "write index: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return model.NewError(model.CodeIO, false, "rename index: %v", err)
	}
	return nil
}

// Load reads an index and rejects incomplete indexes or digest mismatches so
// callers never reuse a stale or half-written scan.
func Load(path, expectedDigest string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewError(model.CodeIO, false, "read index: %v", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, model.NewError(model.CodeIO, false, "parse index: %v", err)
	}
	if !idx.Complete {
		return nil, model.NewError(model.CodeIndexIncomplete, true, "index lacks completion marker")
	}
	if idx.Digest != expectedDigest {
		return nil, model.NewError(model.CodeDigestMismatch, true,
			"input changed: index=%s input=%s", idx.Digest, expectedDigest)
	}
	return &idx, nil
}
