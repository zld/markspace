package ingest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// BoxHeader is a top-level or nested box with absolute byte bounds.
type BoxHeader struct {
	Type       string
	Start      int64
	Payload    int64
	End        int64
	HeaderSize int64
	ExtendsEOF bool
}

// MP4Sample is one decoded-frame descriptor. Times are in track ticks.
type MP4Sample struct {
	Number    int       `json:"number"`
	DTS       int64     `json:"dts"`
	PTS       int64     `json:"pts"`
	Duration  uint32    `json:"duration"`
	Size      uint32    `json:"size"`
	Sync      bool      `json:"sync"`
	Offset    int64     `json:"offset"`
	Span      core.Span `json:"span"`
	Truncated bool      `json:"truncated"` // sample bytes run past the asset
}

// MP4Track carries the parsed sample table for one track.
type MP4Track struct {
	TrackID   uint32      `json:"track_id"`
	Timescale uint32      `json:"timescale"`
	Handler   string      `json:"handler"`
	Codec     string      `json:"codec"`
	Samples   []MP4Sample `json:"-"`
	SyncCount int         `json:"sync_count"`
	Span      core.Span   `json:"span"`
}

// MP4File is the parsed form of one fragmented-free MP4 asset.
type MP4File struct {
	Asset          string          `json:"asset"`
	MajorBrand     string          `json:"major_brand"`
	MovieTimescale uint32          `json:"movie_timescale"`
	Tracks         []MP4Track      `json:"tracks"`
	Issues         diagnose.Issues `json:"-"`
	BoxCount       int             `json:"box_count"`
	Partial        bool            `json:"partial"` // some box could not be parsed
}

// maxSamples bounds sample expansion so a corrupt stts cannot exhaust memory.
const maxSamples = 4 << 20

// ParseMP4 parses an MP4 asset. Structural damage is reported with absolute
// byte spans and parsing continues at the next plausible box, so one bad
// length field does not hide later tracks.
func ParseMP4(asset *Asset) (*MP4File, error) {
	f := &MP4File{Asset: asset.Name}
	boxes, issues, partial := scanBoxes(asset.Data, 0, int64(len(asset.Data)), "", f.Asset, "mp4")
	f.Issues = append(f.Issues, issues...)
	f.Partial = partial
	f.BoxCount = countBoxes(boxes)
	for _, b := range boxes {
		switch b.Type {
		case "ftyp":
			if b.End-b.Payload >= 8 {
				f.MajorBrand = strings.TrimSpace(strings.TrimRight(string(asset.Data[b.Payload:b.Payload+4]), "\x00"))
			}
		case "moov":
			f.parseMoov(asset, b)
		}
	}
	return f, nil
}

func countBoxes(boxes []BoxHeader) int { return len(boxes) }

// scanBoxes walks sibling boxes in [start,end). A malformed length is reported
// once and the scanner resynchronizes on the next recognizable box header.
func scanBoxes(data []byte, start, end int64, parent, asset, format string) ([]BoxHeader, diagnose.Issues, bool) {
	var (
		out     []BoxHeader
		issues  diagnose.Issues
		partial bool
	)
	off := start
	for off < end {
		if end-off < 8 {
			issues.Add(diagnose.Issue{
				Category: diagnose.CatTruncatedBox, Stage: diagnose.StageIngest,
				Format: format, Asset: asset,
				Span:   core.Span{Start: off, End: end},
				Detail: fmt.Sprintf("trailing %d bytes cannot hold a box header", end-off),
			})
			partial = true
			break
		}
		size := int64(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		header := int64(8)
		extends := false
		switch {
		case size == 0:
			size = end - off
			extends = true
		case size == 1:
			if end-off < 16 {
				issues.Add(diagnose.Issue{
					Category: diagnose.CatTruncatedBox, Stage: diagnose.StageIngest,
					Format: format, Asset: asset,
					Span:   core.Span{Start: off, End: end},
					Detail: "truncated 64-bit box header",
				})
				partial = true
				off = end
				continue
			}
			header = 16
			size = int64(binary.BigEndian.Uint64(data[off+8:]))
			if size < 0 {
				size = math.MaxInt64
			}
		}
		switch {
		case !printableType(typ):
			issues.Add(diagnose.Issue{
				Category: diagnose.CatMP4Length, Stage: diagnose.StageIngest,
				Format: format, Asset: asset,
				Span:   core.Span{Start: off, End: off + 8},
				Detail: fmt.Sprintf("non-printable box type %q (declared size %d)", typ, size),
			})
			partial = true
			next := findNextBox(data, off+1, end)
			if next < 0 {
				return out, issues, partial
			}
			off = next
			continue
		case size < header:
			issues.Add(diagnose.Issue{
				Category: diagnose.CatMP4Length, Stage: diagnose.StageIngest,
				Format: format, Asset: asset,
				Span:   core.Span{Start: off, End: off + header},
				Detail: fmt.Sprintf("box %q declares impossible size %d", typ, size),
			})
			partial = true
			next := findNextBox(data, off+header, end)
			if next < 0 {
				return out, issues, partial
			}
			off = next
			continue
		case off+size > end:
			issues.Add(diagnose.Issue{
				Category: diagnose.CatTruncatedBox, Stage: diagnose.StageIngest,
				Format: format, Asset: asset,
				Span:   core.Span{Start: off, End: end},
				Detail: fmt.Sprintf("box %q declares size %d but only %d bytes remain", typ, size, end-off),
			})
			partial = true
			off = end
			continue
		}
		b := BoxHeader{
			Type:       typ,
			Start:      off,
			Payload:    off + header,
			End:        off + size,
			HeaderSize: header,
			ExtendsEOF: extends,
		}
		out = append(out, b)
		if isContainer(typ) {
			children, childIssues, childPartial := scanBoxes(data, b.Payload, b.End, typ, asset, format)
			out = append(out, children...)
			issues = append(issues, childIssues...)
			partial = partial || childPartial
		}
		off = b.End
	}
	return out, issues, partial
}

// knownTypes limits resynchronization to plausible MP4 box names so random
// payload bytes are not mistaken for box headers.
var knownTypes = map[string]bool{
	"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true,
	"moof": true, "mfra": true, "sidx": true, "styp": true, "mvhd": true,
	"trak": true, "tkhd": true, "mdia": true, "mdhd": true, "hdlr": true,
	"minf": true, "vmhd": true, "smhd": true, "dinf": true, "dref": true,
	"stbl": true, "stsd": true, "stts": true, "ctts": true, "stss": true,
	"stsc": true, "stsz": true, "stco": true, "co64": true, "avc1": true,
	"avcC": true, "mp4a": true, "esds": true, "edts": true, "elst": true,
	"udta": true, "meta": true, "ilst": true, "wav": true, "hdl": true,
}

func findNextBox(data []byte, from, end int64) int64 {
	// First pass: a well-formed header that fits inside the parent. Second
	// pass: a plausible known type whose declared size overruns, so the main
	// loop can still report it as a truncated box instead of losing the tail.
	for off := from; off+8 <= end; off++ {
		if isPlausibleHeader(data[off:], end, off, true) {
			return off
		}
	}
	for off := from; off+8 <= end; off++ {
		if isPlausibleHeader(data[off:], end, off, false) {
			return off
		}
	}
	return -1
}

func isPlausibleHeader(b []byte, end, off int64, requireFit bool) bool {
	size := int64(binary.BigEndian.Uint32(b))
	typ := string(b[4:8])
	if !knownTypes[typ] || !printableType(typ) {
		return false
	}
	if size == 0 || size == 1 {
		return true
	}
	if size < 8 {
		return false
	}
	return !requireFit || off+size <= end
}

func printableType(t string) bool {
	if len(t) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if t[i] < 0x20 || t[i] > 0x7e {
			return false
		}
	}
	return true
}

func isContainer(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "moof", "traf", "mvex":
		return true
	}
	return false
}

func (f *MP4File) parseMoov(asset *Asset, moov BoxHeader) {
	data := asset.Data
	children := boxesIn(data, moov)
	for _, b := range children {
		if b.Type != "mvhd" {
			continue
		}
		var mvhd mp4.Mvhd
		if !f.unmarshal(asset, b, &mvhd) {
			continue
		}
		f.MovieTimescale = mvhd.Timescale
	}
	for _, b := range children {
		if b.Type == "trak" {
			if tr, ok := f.parseTrak(asset, b); ok {
				f.Tracks = append(f.Tracks, tr)
			}
		}
	}
}

// boxesIn returns the direct children of a container box. Header-level
// findings are not collected here: the initial full-tree scan in ParseMP4 has
// already reported each one exactly once, and re-reporting them on every
// structural walk would duplicate issues for a single damaged box.
func boxesIn(data []byte, parent BoxHeader) []BoxHeader {
	all, _, _ := scanBoxes(data, parent.Payload, parent.End, parent.Type, "", "mp4")
	direct := make([]BoxHeader, 0, len(all))
	// scanBoxes returns a pre-order flattening: a box is a direct child when
	// it is not nested inside another box that starts later than the parent.
	for i, b := range all {
		nested := false
		for j, o := range all {
			if i == j {
				continue
			}
			if o.Start < b.Start && b.Start >= o.Payload && b.End <= o.End {
				nested = true
				break
			}
		}
		if !nested {
			direct = append(direct, b)
		}
	}
	sort.SliceStable(direct, func(i, j int) bool { return direct[i].Start < direct[j].Start })
	return direct
}

func (f *MP4File) unmarshal(asset *Asset, b BoxHeader, dst mp4.IBox) bool {
	n := b.End - b.Payload
	if n <= 0 {
		return false
	}
	br := bytes.NewReader(asset.Data[b.Payload:b.End])
	if _, err := mp4.Unmarshal(br, uint64(n), dst, mp4.Context{}); err != nil {
		f.Issues.Add(diagnose.Issue{
			Category: diagnose.CatMP4Length, Stage: diagnose.StageIngest,
			Format: "mp4", Asset: asset.Name,
			Span:   core.Span{Start: b.Start, End: b.End},
			Detail: fmt.Sprintf("box %q payload unreadable: %v", b.Type, err),
		})
		f.Partial = true
		return false
	}
	return true
}

func (f *MP4File) parseTrak(asset *Asset, trak BoxHeader) (MP4Track, bool) {
	tr := MP4Track{}
	var mdhd *mp4.Mdhd
	var hdlr *mp4.Hdlr
	var stts *mp4.Stts
	var ctts *mp4.Ctts
	var stss *mp4.Stss
	var stsz *mp4.Stsz
	var stsc *mp4.Stsc
	var chunkOffsets []uint64
	var codec string

	var walk func(b BoxHeader)
	walk = func(b BoxHeader) {
		switch b.Type {
		case "tkhd":
			var box mp4.Tkhd
			if f.unmarshal(asset, b, &box) {
				tr.TrackID = box.TrackID
			}
		case "mdhd":
			var box mp4.Mdhd
			if f.unmarshal(asset, b, &box) {
				mdhd = &box
			}
		case "hdlr":
			var box mp4.Hdlr
			if f.unmarshal(asset, b, &box) {
				hdlr = &box
			}
		case "stts":
			var box mp4.Stts
			if f.unmarshal(asset, b, &box) {
				stts = &box
			}
		case "ctts":
			var box mp4.Ctts
			if f.unmarshal(asset, b, &box) {
				ctts = &box
			}
		case "stss":
			var box mp4.Stss
			if f.unmarshal(asset, b, &box) {
				stss = &box
			}
		case "stsz":
			var box mp4.Stsz
			if f.unmarshal(asset, b, &box) {
				stsz = &box
			}
		case "stsc":
			var box mp4.Stsc
			if f.unmarshal(asset, b, &box) {
				stsc = &box
			}
		case "stco":
			var box mp4.Stco
			if f.unmarshal(asset, b, &box) {
				for _, o := range box.ChunkOffset {
					chunkOffsets = append(chunkOffsets, uint64(o))
				}
			}
		case "co64":
			var box mp4.Co64
			if f.unmarshal(asset, b, &box) {
				chunkOffsets = append(chunkOffsets, box.ChunkOffset...)
			}
		case "stsd":
			codec = f.sampleEntryType(asset, b)
		}
		if isContainer(b.Type) {
			for _, c := range boxesIn(asset.Data, b) {
				walk(c)
			}
		}
	}
	walk(trak)
	if mdhd == nil || mdhd.Timescale == 0 {
		f.Issues.Add(diagnose.Issue{
			Category: diagnose.CatMP4Length, Stage: diagnose.StageIngest,
			Format: "mp4", Asset: asset.Name,
			Span:   core.Span{Start: trak.Start, End: trak.End},
			Detail: "track has no usable mdhd timescale",
		})
		f.Partial = true
		return tr, false
	}
	tr.Timescale = mdhd.Timescale
	tr.Codec = codec
	if hdlr != nil {
		tr.Handler = strings.TrimRight(string(hdlr.HandlerType[:]), "\x00")
	}
	tr.Span = core.Span{Start: trak.Start, End: trak.End}
	if stts == nil || stsz == nil {
		f.Issues.Add(diagnose.Issue{
			Category: diagnose.CatTruncatedBox, Stage: diagnose.StageIngest,
			Format: "mp4", Asset: asset.Name,
			Span:   core.Span{Start: trak.Start, End: trak.End},
			Detail: "track is missing stts or stsz",
		})
		f.Partial = true
		return tr, false
	}
	tr.Samples, tr.SyncCount = f.expandSamples(asset, trak, tr.Timescale, stts, ctts, stss, stsz, stsc, chunkOffsets)
	return tr, len(tr.Samples) > 0
}

func (f *MP4File) sampleEntryType(asset *Asset, stsd BoxHeader) string {
	payload := stsd.Payload
	if payload+8 > stsd.End {
		return ""
	}
	// FullBox(4) + entry_count(4), then the first entry box.
	entry := payload + 8
	if entry+8 > stsd.End {
		return ""
	}
	t := string(asset.Data[entry+4 : entry+8])
	if printableType(t) {
		return t
	}
	return ""
}

// expandSamples flattens stts/ctts/stsz into per-sample records and maps them
// onto file offsets through stco/stsc. All decoding is bounded by maxSamples.
func (f *MP4File) expandSamples(asset *Asset, trak BoxHeader, timescale uint32, stts *mp4.Stts, ctts *mp4.Ctts, stss *mp4.Stss, stsz *mp4.Stsz, stsc *mp4.Stsc, chunks []uint64) ([]MP4Sample, int) {
	var durs []uint32
	for _, e := range stts.Entries {
		n := int(e.SampleCount)
		if n <= 0 {
			continue
		}
		if len(durs)+n > maxSamples {
			f.Issues.Add(diagnose.Issue{
				Category: diagnose.CatIndexOutOfRange, Stage: diagnose.StageIngest,
				Format: "mp4", Asset: asset.Name,
				Span:   core.Span{Start: trak.Start, End: trak.End},
				Detail: "sample count exceeds supported limit",
			})
			f.Partial = true
			n = maxSamples - len(durs)
		}
		for i := 0; i < n; i++ {
			durs = append(durs, e.SampleDelta)
		}
	}
	sizes := stsz.EntrySize
	total := len(durs)
	if stsz.SampleSize != 0 {
		total = int(stsz.SampleCount)
		if total > maxSamples {
			total = maxSamples
			f.Partial = true
		}
		sizes = nil
	} else if len(sizes) < total {
		total = len(sizes)
	}
	if total > len(durs) {
		// Stts shorter than stsz: keep the known part and report.
		f.Issues.Add(diagnose.Issue{
			Category: diagnose.CatTruncatedBox, Stage: diagnose.StageIngest,
			Format: "mp4", Asset: asset.Name,
			Span:   core.Span{Start: trak.Start, End: trak.End},
			Detail: fmt.Sprintf("stts covers %d samples, stsz declares %d", len(durs), total),
		})
		f.Partial = true
		total = len(durs)
	}
	offsets := sampleOffsets(chunks, stsc, sizes, stsz.SampleSize, total)
	presync := make(map[int]bool, 0)
	if stss != nil {
		for _, sn := range stss.SampleNumber {
			if sn >= 1 && int(sn) <= total {
				presync[int(sn)-1] = true
			}
		}
	}
	cttsOffsets := expandCTTS(ctts, total)
	samples := make([]MP4Sample, 0, total)
	assetSize := int64(len(asset.Data))
	var dts int64
	syncCount := 0
	// Consecutive out-of-range samples are reported once per run so a single
	// cut in mdat does not produce thousands of near-identical findings.
	runStart, runCount := 0, 0
	runSpan := core.Span{Start: trak.Start, End: trak.End}
	flushRun := func(endSample int) {
		if runCount == 0 {
			return
		}
		f.Issues.Add(diagnose.Issue{
			Category: diagnose.CatIndexOutOfRange, Stage: diagnose.StageIngest,
			Format: "mp4", Asset: asset.Name,
			Span:   runSpan,
			Detail: fmt.Sprintf("samples %d..%d reference bytes past the end of the asset", runStart, endSample),
		})
		f.Partial = true
		runStart, runCount = 0, 0
		runSpan = core.Span{Start: trak.Start, End: trak.End}
	}
	for i := 0; i < total; i++ {
		size := stsz.SampleSize
		if sizes != nil {
			size = sizes[i]
		}
		sync := stss == nil || presync[i]
		if sync {
			syncCount++
		}
		off := offsets[i]
		span := core.Span{Start: off, End: off + int64(size)}
		s := MP4Sample{
			Number:   i + 1,
			DTS:      dts,
			PTS:      dts + cttsOffsets[i],
			Duration: durs[i],
			Size:     size,
			Sync:     sync,
			Offset:   off,
			Span:     span,
		}
		if off < 0 || off > assetSize || span.End > assetSize {
			if runCount == 0 {
				runStart = i + 1
				// Anchor the finding at the exact cut: the first byte the
				// sample table expects but the asset no longer has.
				if off >= 0 && off <= assetSize {
					runSpan = core.Span{Start: off, End: assetSize}
				} else {
					runSpan = core.Span{Start: assetSize, End: assetSize}
				}
			}
			runCount++
			s.Truncated = true
			if off < 0 {
				s.Offset = 0
				s.Span = core.Span{Start: 0, End: 0}
			} else {
				// Clamp to the readable prefix so span invariants hold; the
				// clamping boundary is exactly the damaged cut.
				if off > assetSize {
					off = assetSize
				}
				s.Offset = off
				s.Span = core.Span{Start: off, End: assetSize}
			}
		} else {
			// The run ends at the previous sample, whose number is i.
			flushRun(i)
		}
		dts += int64(durs[i])
		samples = append(samples, s)
	}
	flushRun(total)
	return samples, syncCount
}

func expandCTTS(ctts *mp4.Ctts, total int) []int64 {
	out := make([]int64, total)
	if ctts == nil {
		return out
	}
	i := 0
	for ci := range ctts.Entries {
		e := ctts.Entries[ci]
		for k := uint32(0); k < e.SampleCount && i < total; k++ {
			out[i] = int64(ctts.Entries[ci].SampleOffsetV1)
			if ctts.GetVersion() == 0 {
				out[i] = int64(e.SampleOffsetV0)
			}
			i++
		}
		if i >= total {
			break
		}
	}
	return out
}

// sampleOffsets maps samples to absolute file offsets using the chunk table.
// Missing chunk information yields -1 entries rather than guessing.
func sampleOffsets(chunks []uint64, stsc *mp4.Stsc, sizes []uint32, fixedSize uint32, total int) []int64 {
	out := make([]int64, total)
	for i := range out {
		out[i] = -1
	}
	if stsc == nil || len(chunks) == 0 {
		return out
	}
	entries := make([]mp4.StscEntry, len(stsc.Entries))
	copy(entries, stsc.Entries)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].FirstChunk < entries[j].FirstChunk })
	sample := 0
	for ei, e := range entries {
		lastChunk := int64(len(chunks))
		if ei+1 < len(entries) {
			lastChunk = int64(entries[ei+1].FirstChunk) - 1
		}
		for ci := int64(e.FirstChunk) - 1; ci >= 0 && ci < lastChunk && ci < int64(len(chunks)); ci++ {
			off := int64(chunks[ci])
			for k := uint32(0); k < e.SamplesPerChunk && sample < total; k++ {
				size := int64(fixedSize)
				if sizes != nil && sample < len(sizes) {
					size = int64(sizes[sample])
				}
				out[sample] = off
				off += size
				sample++
			}
			if sample >= total {
				return out
			}
		}
	}
	return out
}
