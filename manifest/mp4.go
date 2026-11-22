package manifest

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/abema/go-mp4"

	"medpipe/timeline"
)

// manifestBrand identifies medpipe manifest carriers inside the ftyp box.
const manifestBrand = "mdpf"

// WriteMP4 serializes the manifest as canonical JSON and writes a minimal
// ISO BMFF file: an ftyp box declaring the medpipe brand plus an mdat box
// carrying the JSON payload. No media sample tables are produced; the box
// structure is still standard and parseable with go-mp4.
func WriteMP4(w io.WriteSeeker, m Manifest) error {
	payload, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, "",
			"manifest json failed: %v", err)
	}
	mw := mp4.NewWriter(w)
	// ftyp: major brand mdpf, minor version 0, same compatible brand.
	if _, err = mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   toBrand(manifestBrand),
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: toBrand("iso4")},
			{CompatibleBrand: toBrand(manifestBrand)},
		},
	}
	if _, err = mp4.Marshal(mw, ftyp, mp4.Context{}); err != nil {
		return err
	}
	if _, err = mw.EndBox(); err != nil {
		return err
	}
	// mdat: the deterministic JSON manifest payload.
	if _, err = mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	mdat := &mp4.Mdat{Data: payload}
	if _, err = mp4.Marshal(mw, mdat, mp4.Context{}); err != nil {
		return err
	}
	if _, err = mw.EndBox(); err != nil {
		return err
	}
	return nil
}

// ReadMP4 parses a manifest carrier back into a Manifest. A missing mdat box,
// a wrong brand or invalid JSON all produce located manifest errors.
func ReadMP4(r io.ReadSeeker) (Manifest, error) {
	var (
		raw     []byte
		brands  [][4]byte
	)
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "ftyp":
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if f, ok := box.(*mp4.Ftyp); ok {
				brands = append(brands, f.MajorBrand)
			}
		case "mdat":
			var buf bytes.Buffer
			if _, perr := h.ReadData(&buf); perr != nil {
				return nil, perr
			}
			raw = buf.Bytes()
		}
		return nil, nil
	})
	if err != nil {
		return Manifest{}, timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, "",
			"mp4 parse failed: %v", err)
	}
	brandOK := false
	for _, b := range brands {
		if string(b[:]) == manifestBrand {
			brandOK = true
		}
	}
	if !brandOK {
		return Manifest{}, timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, "",
			"ftyp missing %q brand", manifestBrand)
	}
	if raw == nil {
		return Manifest{}, timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, "",
			"mdat manifest payload missing")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, "",
			"manifest json invalid: %v", err)
	}
	return m, nil
}

// VerifyDuration re-checks every track's recorded duration against the clock
// and returns a located error on the first mismatch.
func VerifyDuration(m Manifest, clock timeline.Clock) error {
	for _, tr := range m.Tracks {
		want := clock.SamplesToNanos(tr.OutputFrames)
		if tr.DurationNanos != want {
			return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, tr.TrackID,
				"recorded %d ns != clock-derived %d ns", tr.DurationNanos, want)
		}
		if tr.AlignmentMillis < 0 || tr.AlignmentMillis > 1 {
			return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, tr.TrackID,
				"alignment %d ms outside +/-1 ms", tr.AlignmentMillis)
		}
	}
	return nil
}

func toBrand(s string) [4]byte {
	var b [4]byte
	copy(b[:], s)
	return b
}
