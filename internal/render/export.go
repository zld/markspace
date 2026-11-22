package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"avcheck/internal/media"
	"avcheck/internal/timeline"
)

// FrameRecord is the per-frame provenance entry stored in the manifest.
// Times are rational strings so comparison never depends on float rounding.
type FrameRecord struct {
	Index    uint64            `json:"index"`
	File     string            `json:"file"`
	Time     string            `json:"time"`
	Tick     string            `json:"tick"` // time scaled to the clock timescale
	Primary  uint32            `json:"primary_track"`
	Key      bool              `json:"key"`
	InputSum string            `json:"input_sha256,omitempty"`
	States   map[string]string `json:"states"` // track id -> clean|recovered|placeholder|degraded
	SHA256   string            `json:"sha256"` // sha256 of the PNG file
}

// SegmentRecord summarizes one exported segment.
type SegmentRecord struct {
	Index        int    `json:"index"`
	Frames       int    `json:"frames"`
	FirstIndex   uint64 `json:"first_index"`
	LastIndex    uint64 `json:"last_index"`
	FirstTime    string `json:"first_time"` // rational seconds
	LastTime     string `json:"last_time"`
	Recovered    int    `json:"recovered"`
	Placeholders int    `json:"placeholders"`
	Keyframes    int    `json:"keyframes"`
	SHA256       string `json:"sha256"`
}

// TrackInfo identifies the MP4 index written for one input track.
type TrackInfo struct {
	ID         uint32 `json:"id"`
	MP4        string `json:"mp4"` // relative to the export directory
	Samples    int    `json:"samples"`
	StartTicks int64  `json:"start_ticks"`
}

// InputStats carries the recovery tally of the producing session so a later
// verify run can classify the input without re-running the pipeline.
type InputStats struct {
	Received   uint64 `json:"received"`
	Duplicates uint64 `json:"duplicates"`
	Reordered  uint64 `json:"reordered"`
	Late       uint64 `json:"late"`
	Gaps       uint64 `json:"gaps"`
	BurstGaps  uint64 `json:"burst_gaps"`
	Corrupt    uint64 `json:"corrupt"`
	Truncated  uint64 `json:"truncated"`
	Rejected   uint64 `json:"rejected"`
	Dropped    uint64 `json:"dropped"`
}

// Damaged reports whether any input frame had to be rejected.
func (s InputStats) Damaged() bool { return s.Corrupt+s.Truncated+s.Rejected > 0 }

// Manifest is the deterministic export summary; rerunning the same input
// must produce an identical manifest.
type Manifest struct {
	Version   int             `json:"version"`
	Tracks    int             `json:"tracks"`
	Frames    int             `json:"frames"`
	Segments  []SegmentRecord `json:"segments"`
	Records   []FrameRecord   `json:"records"`
	TrackInfo []TrackInfo     `json:"track_info,omitempty"`
	Inputs    InputStats      `json:"inputs"`
	Summary   string          `json:"summary"` // sha256 over segment hashes
	Digest    string          `json:"digest"`  // sha256 over the manifest body
}

// Renderer turns an output frame into an image.
type Renderer func(of timeline.OutputFrame) *image.RGBA

// Options configures one export run.
type Options struct {
	SegFrames int
	Clock     media.Clock
	Tracks    []TrackInfo
	Inputs    InputStats
}

// Writer streams output frames to PNG files and one manifest. Memory is
// bounded by one segment of encoded PNGs regardless of the input length.
type Writer struct {
	dir       string
	opt       Options
	tracks    int
	segFrames int
	render    Renderer

	m       *Manifest
	cur     *SegmentRecord
	curPix  [][]byte
	frameNo int

	RoundtripOK  int
	RoundtripBad int
}

// NewWriter prepares dir and an empty manifest.
func NewWriter(dir string, tracks int, opt Options, r Renderer) (*Writer, error) {
	if opt.SegFrames <= 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "segment size %d", opt.SegFrames)
	}
	if tracks < 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "negative track count")
	}
	if r == nil {
		return nil, media.NewError(media.KindState, 0, 0, -1, "nil renderer")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Writer{
		dir: dir, opt: opt, tracks: tracks, segFrames: opt.SegFrames, render: r,
		m: &Manifest{Version: 2, Tracks: tracks, Records: []FrameRecord{},
			Segments: []SegmentRecord{}, TrackInfo: opt.Tracks, Inputs: opt.Inputs},
	}, nil
}

// Add writes one output frame and updates the in-progress segment.
func (w *Writer) Add(of timeline.OutputFrame) error {
	img := w.render(of)
	if img == nil {
		return media.NewError(media.KindState, 0, of.Index, -1, "renderer returned nil image")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return media.NewError(media.KindCorrupt, 0, of.Index, -1, "png encode: %v", err)
	}
	if err := w.checkRoundtrip(&buf, img, of.Index); err != nil {
		return err
	}
	raw := buf.Bytes()
	name := fmt.Sprintf("frame_%05d.png", w.frameNo)
	if err := os.WriteFile(filepath.Join(w.dir, name), raw, 0o644); err != nil {
		return err
	}
	fr := FrameRecord{
		Index: of.Index, File: name, Time: of.Time.String(),
		States: make(map[string]string, len(of.Sources)),
		SHA256: Hex(SumSegment([][]byte{raw})),
	}
	if of.Primary != nil {
		fr.Primary, fr.Key = of.Primary.StreamID, of.Primary.Key
		fr.InputSum = Hex(of.Primary.Sum)
	}
	if w.opt.Clock.Timescale > 0 {
		tk, err := w.opt.Clock.Ticks(of.Time)
		if err != nil {
			return err
		}
		fr.Tick = fmt.Sprintf("%d", tk)
	}
	if w.cur == nil {
		w.cur = &SegmentRecord{Index: len(w.m.Segments), FirstIndex: of.Index, FirstTime: of.Time.String()}
	}
	w.cur.Frames++
	w.cur.LastIndex, w.cur.LastTime = of.Index, of.Time.String()
	for _, s := range of.Sources {
		fr.States[fmt.Sprintf("%d", s.TrackID)] = s.Frame.String()
		switch s.Frame {
		case media.StateRecovered:
			w.cur.Recovered++
		case media.StatePlaceholder:
			w.cur.Placeholders++
		}
	}
	if fr.Key {
		w.cur.Keyframes++
	}
	w.m.Records = append(w.m.Records, fr)
	w.curPix = append(w.curPix, raw)
	w.m.Frames++
	w.frameNo++
	if w.cur.Frames >= w.segFrames {
		w.closeSegment()
	}
	return nil
}

// checkRoundtrip decodes the just-encoded PNG and compares pixels with the
// rendered image, proving the export is lossless and reproducible. Opaque
// frames take the fast path where premultiplied and straight alpha agree.
func (w *Writer) checkRoundtrip(buf *bytes.Buffer, img *image.RGBA, index uint64) error {
	dec, err := png.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		w.RoundtripBad++
		return media.NewError(media.KindCorrupt, 0, index, -1, "png decode: %v", err)
	}
	if dec.Bounds() != img.Bounds() {
		w.RoundtripBad++
		return media.NewError(media.KindCorrupt, 0, index, -1,
			"png bounds %v != %v", dec.Bounds(), img.Bounds())
	}
	if img.Opaque() {
		if !samePix(img, dec) {
			w.RoundtripBad++
			return media.NewError(media.KindCorrupt, 0, index, -1, "png pixels differ after roundtrip")
		}
		w.RoundtripOK++
		return nil
	}
	// PNG stores straight alpha; compare against the same conversion the
	// encoder applies instead of raw premultiplied bytes.
	if !sameNRGBA(img, dec) {
		w.RoundtripBad++
		return media.NewError(media.KindCorrupt, 0, index, -1, "png pixels differ after roundtrip")
	}
	w.RoundtripOK++
	return nil
}

func samePix(want *image.RGBA, got image.Image) bool {
	switch g := got.(type) {
	case *image.RGBA:
		return bytes.Equal(want.Pix, g.Pix)
	case *image.NRGBA:
		// cbTC8 decodes to RGBA and cbTCA8 to NRGBA; alpha 255 makes them
		// byte-identical, so compare the straight-alpha result directly.
		return bytes.Equal(want.Pix, g.Pix)
	}
	return false
}

func sameNRGBA(want *image.RGBA, got image.Image) bool {
	b := want.Bounds()
	nb := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if color.NRGBAModel.Convert(want.At(x, y)) != color.NRGBAModel.Convert(got.At(nb.Min.X+x-b.Min.X, nb.Min.Y+y-b.Min.Y)) {
				return false
			}
		}
	}
	return true
}

func (w *Writer) closeSegment() {
	if w.cur == nil {
		return
	}
	sum := SumSegment(w.curPix)
	w.cur.SHA256 = Hex(sum)
	w.m.Segments = append(w.m.Segments, *w.cur)
	w.cur = nil
	w.curPix = nil
}

// Finish closes the last segment and writes manifest.json.
func (w *Writer) Finish() (*Manifest, error) {
	w.closeSegment()
	var summary [][]byte
	for _, s := range w.m.Segments {
		var sum [32]byte
		if _, err := fmt.Sscanf(s.SHA256, "%64x", &sum); err == nil {
			summary = append(summary, sum[:])
		}
	}
	w.m.Summary = Hex(SumSegment(summary))
	return w.m, saveManifest(w.dir, w.m)
}

// saveManifest fills the digest and writes manifest.json atomically.
func saveManifest(dir string, m *Manifest) error {
	m.Digest = ""
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	m.Digest = Hex(SumSegment([][]byte{body}))
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "manifest.json"))
}

// ExportWith writes every output frame into dir in one call.
func ExportWith(dir string, tracks int, outs []timeline.OutputFrame, opt Options, r Renderer) (*Manifest, error) {
	w, err := NewWriter(dir, tracks, opt, r)
	if err != nil {
		return nil, err
	}
	for _, of := range outs {
		if err := w.Add(of); err != nil {
			return nil, err
		}
	}
	return w.Finish()
}

// Export is ExportWith with only a segment size.
func Export(dir string, tracks int, outs []timeline.OutputFrame, segFrames int, r Renderer) (*Manifest, error) {
	return ExportWith(dir, tracks, outs, Options{SegFrames: segFrames}, r)
}

// LoadManifest reads a manifest.json from dir.
func LoadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, media.NewError(media.KindCorrupt, 0, 0, -1, "manifest: %v", err)
	}
	if len(m.Records) != m.Frames {
		return nil, media.NewError(media.KindCorrupt, 0, 0, -1,
			"manifest record count %d != frames %d", len(m.Records), m.Frames)
	}
	return &m, nil
}

// ManifestDigest recomputes the digest of a loaded manifest.
func ManifestDigest(m *Manifest) string {
	c := *m
	c.Digest = ""
	body, err := json.Marshal(&c)
	if err != nil {
		return ""
	}
	return Hex(SumSegment([][]byte{body}))
}

// Validate performs the structural checks a verifier can do without pixels.
func (m *Manifest) Validate() error {
	if m.Version != 2 {
		return media.NewError(media.KindCorrupt, 0, 0, -1, "manifest version %d", m.Version)
	}
	if ManifestDigest(m) != m.Digest {
		return media.NewError(media.KindCorrupt, 0, 0, -1, "manifest digest mismatch")
	}
	total := 0
	for i, s := range m.Segments {
		if s.Index != i || s.Frames <= 0 {
			return media.NewError(media.KindCorrupt, 0, 0, -1, "segment %d malformed", i)
		}
		total += s.Frames
	}
	if total != m.Frames {
		return media.NewError(media.KindCorrupt, 0, 0, -1,
			"segment frames %d != manifest frames %d", total, m.Frames)
	}
	return nil
}

// VerifySegments recomputes per-segment hashes from the PNGs on disk.
func VerifySegments(dir string, m *Manifest) (matched int, err error) {
	frameAt := 0
	for si := range m.Segments {
		rec := &m.Segments[si]
		var paths []string
		for k := 0; k < rec.Frames; k++ {
			paths = append(paths, filepath.Join(dir, fmt.Sprintf("frame_%05d.png", frameAt+k)))
		}
		frameAt += rec.Frames
		sum, err := SumFiles(paths)
		if err != nil {
			return matched, err
		}
		if Hex(sum) == rec.SHA256 {
			matched++
		}
	}
	return matched, nil
}

// VerifyFrames recomputes every per-frame hash and returns the match ratio.
func VerifyFrames(dir string, m *Manifest) (matched int, err error) {
	for i := range m.Records {
		r := &m.Records[i]
		sum, err := SumFiles([]string{filepath.Join(dir, r.File)})
		if err != nil {
			return matched, err
		}
		if Hex(sum) == r.SHA256 {
			matched++
		}
	}
	return matched, nil
}
