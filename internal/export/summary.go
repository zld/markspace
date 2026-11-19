// Package export renders located landing points (summary + per-sample
// SHA-256) as JSON or line text, and cross-checks our box/sample-table
// interpretation against go-mp4. Cross-check differences are reported
// as diagnostics; they never replace our own parse results.
package export

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mp4seek/internal/box"
	"mp4seek/internal/index"
	"mp4seek/internal/movie"
	"mp4seek/internal/seek"
)

// Record is one exported landing point.
type Record struct {
	Track       int     `json:"track"`
	Sample      int     `json:"sample"`
	PTSSeconds  float64 `json:"pts_seconds"`
	PTS         int64   `json:"pts"`
	DTS         int64   `json:"dts"`
	CTS         int64   `json:"cts"`
	FrameType   string  `json:"frame_type"` // "sync" or "non-sync"
	Offset      int64   `json:"offset"`
	Size        int64   `json:"size"`
	SHA256      string  `json:"sha256"`
}

// NewRecord builds a record from a locate result, hashing only the
// landed sample's bytes (never the whole mdat).
func NewRecord(rd *box.Reader, ti *index.TrackIndex, r *seek.Result) (Record, error) {
	sum, err := HashSample(rd, r.Offset, r.Size)
	if err != nil {
		return Record{}, err
	}
	ft := "non-sync"
	if r.Sync {
		ft = "sync"
	}
	ts := ti.Track.Timescale
	if ts == 0 {
		ts = 1
	}
	return Record{
		Track: r.TrackID, Sample: r.Sample,
		PTSSeconds: float64(r.PTS) / float64(ts),
		PTS: r.PTS, DTS: r.DTS, CTS: r.CTS,
		FrameType: ft, Offset: r.Offset, Size: r.Size, SHA256: sum,
	}, nil
}

// HashSample reads exactly the sample extent and returns its SHA-256 hex.
func HashSample(rd *box.Reader, off, size int64) (string, error) {
	if off < 0 || size < 0 || off+size > rd.Size() {
		return "", fmt.Errorf("sample extent [%d,%d) out of file bounds %d", off, off+size, rd.Size())
	}
	if size > box.MaxPayload {
		return "", fmt.Errorf("sample size %d exceeds %d byte limit", size, box.MaxPayload)
	}
	buf := make([]byte, size)
	if _, err := rd.ReadAt(buf, off); err != nil {
		return "", fmt.Errorf("read sample at offset %d: %w", off, err)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// WriteJSON writes records as a JSON array.
func WriteJSON(w io.Writer, recs []Record) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(recs)
}

// WriteLines writes one record per line in a stable column layout.
func WriteLines(w io.Writer, recs []Record) error {
	for _, r := range recs {
		if _, err := fmt.Fprintf(w, "track=%d sample=%d pts=%.6f dts=%d type=%s offset=%d size=%d sha256=%s\n",
			r.Track, r.Sample, r.PTSSeconds, r.DTS, r.FrameType, r.Offset, r.Size, r.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// CrossCheck re-reads the file with go-mp4 and compares box headers and
// sample-table counts against our own parse. Each difference becomes a
// diagnostic string; a clean run returns nil.
func CrossCheck(path string, m *movie.Movie, idx []*index.TrackIndex) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var diags []string
	trakNo := -1
	type counts struct {
		stts, stss, chunks, samples int
		hasStss                     bool
	}
	var got []counts

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTrak():
			trakNo++
			got = append(got, counts{})
			return h.Expand()
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeEdts():
			return h.Expand()
		case mp4.BoxTypeStts():
			p, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			got[trakNo].stts = len(p.(*mp4.Stts).Entries)
		case mp4.BoxTypeStss():
			p, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			got[trakNo].hasStss = true
			got[trakNo].stss = len(p.(*mp4.Stss).SampleNumber)
		case mp4.BoxTypeStsz():
			p, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			got[trakNo].samples = int(p.(*mp4.Stsz).SampleCount)
		case mp4.BoxTypeStco():
			p, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			got[trakNo].chunks = len(p.(*mp4.Stco).ChunkOffset)
		case mp4.BoxTypeCo64():
			p, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			got[trakNo].chunks = len(p.(*mp4.Co64).ChunkOffset)
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("go-mp4 cross-check: %w", err)
	}

	if len(got) != len(m.Tracks) {
		diags = append(diags, fmt.Sprintf("track count: ours=%d go-mp4=%d", len(m.Tracks), len(got)))
	}
	for i := 0; i < len(got) && i < len(m.Tracks); i++ {
		tr := m.Tracks[i]
		g := got[i]
		if g.stts != len(tr.Stts) {
			diags = append(diags, fmt.Sprintf("trak %d stts entries: ours=%d go-mp4=%d", tr.ID, len(tr.Stts), g.stts))
		}
		if g.samples != len(tr.Sizes) {
			diags = append(diags, fmt.Sprintf("trak %d stsz count: ours=%d go-mp4=%d", tr.ID, len(tr.Sizes), g.samples))
		}
		if g.chunks != len(tr.Chunks) {
			diags = append(diags, fmt.Sprintf("trak %d chunk count: ours=%d go-mp4=%d", tr.ID, len(tr.Chunks), g.chunks))
		}
		if g.hasStss != tr.HasStss || g.stss != len(tr.Stss) {
			diags = append(diags, fmt.Sprintf("trak %d stss: ours=(%v,%d) go-mp4=(%v,%d)", tr.ID, tr.HasStss, len(tr.Stss), g.hasStss, g.stss))
		}
		if i < len(idx) && g.samples != len(idx[i].Samples) {
			diags = append(diags, fmt.Sprintf("trak %d indexed samples: ours=%d go-mp4=%d", tr.ID, len(idx[i].Samples), g.samples))
		}
	}
	return diags, nil
}
