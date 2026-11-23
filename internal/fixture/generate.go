package fixture

import (
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

// Generate writes a complete, self-consistent archive under cfg.Root and
// returns its index. Re-running with the same seed and fault rewrites identical
// bytes.
func Generate(cfg Config) (*Result, error) {
	cfg.defaults()
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, err
	}
	m := &Manifest{
		Seed:          cfg.Seed,
		Fault:         cfg.Fault,
		Timebase:      DefaultTimebase,
		Segments:      cfg.Segments,
		FramesPerSeg:  cfg.FramesPerSegment(),
		FrameTicks:    cfg.FrameTicks,
		DashFile:      "manifest.mpd",
		HLSFile:       "media.m3u8",
		RangeFile:     "ranges.m3u8",
		RangeMedia:    "media.bin",
		Reps:          cfg.Reps,
		SegmentsByRep: map[string][]SegmentInfo{},
		Files:         map[string]string{},
	}
	for _, rep := range cfg.Reps {
		if err := generateRep(cfg, m, rep); err != nil {
			return nil, err
		}
	}
	if err := applyContentFault(cfg, m); err != nil {
		return nil, err
	}
	if err := writeRangePlaylist(cfg, m); err != nil {
		return nil, err
	}
	if err := writeManifestFiles(cfg, m); err != nil {
		return nil, err
	}
	if _, err := writeJSON(cfg.Root, "fixture.json", m); err != nil {
		return nil, err
	}
	return &Result{Root: cfg.Root, Manifest: m}, nil
}

// generateRep writes one representation's init and media segments plus their
// packet captures.
func generateRep(cfg Config, m *Manifest, rep RepConfig) error {
	ts := rep.Timescale
	if ts <= 0 {
		ts = DefaultTimebase
	}
	initBytes, err := buildInit(ts)
	if err != nil {
		return fmt.Errorf("%s init: %w", rep.ID, err)
	}
	initRel := path.Join(rep.ID, "init.mp4")
	sum, err := writeFile(cfg.Root, initRel, initBytes)
	if err != nil {
		return err
	}
	m.Files[initRel] = sum

	frames := cfg.repFrames(rep)
	per := cfg.FramesPerSegment()
	var infos []SegmentInfo
	for seg := 0; seg < cfg.Segments; seg++ {
		chunk := frames[seg*per : (seg+1)*per]
		data, err := buildSegment(uint32(seg+1), chunk)
		if err != nil {
			return fmt.Errorf("%s seg %d: %w", rep.ID, seg, err)
		}
		uri := path.Join(rep.ID, fmt.Sprintf("seg-%02d.m4s", seg))
		sum, err := writeFile(cfg.Root, uri, data)
		if err != nil {
			return err
		}
		m.Files[uri] = sum
		inOrder := makePackets(rep, DefaultRTPClock, chunk, 0)
		arrivals := applyFault(cfg.Seed, cfg.Fault, rep.ID, inOrder, seg, cfg.Segments)
		capRel := path.Join(rep.ID, fmt.Sprintf("cap-%02d.json", seg))
		capSum, err := writeCapture(cfg.Root, capRel, &Capture{
			Segment:         fmt.Sprintf("%d", seg+1),
			Representation:  rep.ID,
			RTPTimebase:     DefaultRTPClock,
			AnchorTimestamp: inOrder[0].Timestamp,
			Packets:         arrivals,
		})
		if err != nil {
			return err
		}
		m.Files[capRel] = capSum
		infos = append(infos, SegmentInfo{
			ID:         fmt.Sprintf("%d", seg+1),
			URI:        uri,
			Init:       initRel,
			Capture:    capRel,
			Start:      int64(seg) * DefaultSegmentTicks,
			Duration:   DefaultSegmentTicks,
			Size:       int64(len(data)),
			SHA256:     sum,
			CaptureSHA: capSum,
		})
	}
	m.SegmentsByRep[rep.ID] = infos
	return nil
}

// applyContentFault mutates generated media for faults that are not arrival
// order properties.
func applyContentFault(cfg Config, m *Manifest) error {
	switch cfg.Fault {
	case FaultTruncatedBox:
		return truncateSegment(cfg, m, "v-low", cfg.Segments/2)
	case FaultDigestConflict:
		return replaceSegment(cfg, m, "v-low", cfg.Segments/2)
	case FaultKeyframeSkew:
		rep := cfg.Reps[len(cfg.Reps)-1]
		rep.KeyframeGap += 7
		// Regenerate only the low layer's media and captures.
		saved := m.SegmentsByRep[rep.ID]
		_ = saved
		return generateRep(cfg, m, rep)
	}
	return nil
}

func truncateSegment(cfg Config, m *Manifest, rep string, seg int) error {
	infos := m.SegmentsByRep[rep]
	if seg < 0 || seg >= len(infos) {
		return fmt.Errorf("truncate: segment %d out of range", seg)
	}
	target := path.Join(cfg.Root, infos[seg].URI)
	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	cut := len(data) - 37
	if cut <= 0 {
		return fmt.Errorf("truncate: segment too small")
	}
	sum, err := writeFile(cfg.Root, infos[seg].URI, data[:cut])
	if err != nil {
		return err
	}
	m.Files[infos[seg].URI] = sum
	m.SegmentsByRep[rep][seg].Size = int64(cut)
	m.SegmentsByRep[rep][seg].SHA256 = sum
	return nil
}

func replaceSegment(cfg Config, m *Manifest, rep string, seg int) error {
	infos := m.SegmentsByRep[rep]
	if seg < 0 || seg >= len(infos) {
		return fmt.Errorf("replace: segment %d out of range", seg)
	}
	target := path.Join(cfg.Root, infos[seg].URI)
	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	mutated := append([]byte(nil), data...)
	mutated[len(mutated)-1] ^= 0xff
	sum, err := writeFile(cfg.Root, infos[seg].URI, mutated)
	if err != nil {
		return err
	}
	m.Files[infos[seg].URI] = sum
	m.SegmentsByRep[rep][seg].SHA256 = sum
	return nil
}

// writeRangePlaylist writes a byte-range playlist over one concatenated media
// resource, plus that resource. The concatenation is byte-identical to the
// separate segment files, so ranges and files describe the same media.
func writeRangePlaylist(cfg Config, m *Manifest) error {
	rep := cfg.Reps[0]
	var concat []byte
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", DefaultSegmentTicks/DefaultTimebase)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	offset := int64(0)
	for _, info := range m.SegmentsByRep[rep.ID] {
		data, err := os.ReadFile(path.Join(cfg.Root, info.URI))
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "#EXTINF:%d.000,\n#EXT-X-BYTERANGE:%d@%d\n%s\n",
			DefaultSegmentTicks/DefaultTimebase, info.Size, offset, m.RangeMedia)
		concat = append(concat, data...)
		offset += info.Size
	}
	if cfg.Fault == FaultInexactTimebase {
		b.Reset()
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n")
		b.WriteString("#EXTINF:0.033333333,\nseg/0.m4s\n#EXT-X-ENDLIST\n")
	}
	if cfg.Fault == FaultManifestBoundary {
		b.Reset()
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:1\n")
		b.WriteString("#EXTINF:4.000,\n#EXT-X-BYTERANGE:1000\nseg/0.m4s\n#EXT-X-ENDLIST\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	sum, err := writeFile(cfg.Root, m.RangeMedia, concat)
	if err != nil {
		return err
	}
	m.Files[m.RangeMedia] = sum
	sum, err = writeFile(cfg.Root, m.RangeFile, []byte(b.String()))
	if err != nil {
		return err
	}
	m.Files[m.RangeFile] = sum
	return nil
}

// writeManifestFiles writes the DASH MPD and the plain HLS playlist.
func writeManifestFiles(cfg Config, m *Manifest) error {
	mpd := renderMPD(cfg, m)
	sum, err := writeFile(cfg.Root, m.DashFile, []byte(mpd))
	if err != nil {
		return err
	}
	m.Files[m.DashFile] = sum
	if cfg.Fault == FaultInexactTimebase || cfg.Fault == FaultManifestBoundary {
		return nil
	}
	hls := renderHLS(cfg, m)
	sum, err = writeFile(cfg.Root, m.HLSFile, []byte(hls))
	if err != nil {
		return err
	}
	m.Files[m.HLSFile] = sum
	return nil
}

func renderMPD(cfg Config, m *Manifest) string {
	dur := cfg.Segments * DefaultSegmentTicks
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="%s" minBufferTime="PT2S">`+"\n",
		isoDuration(int64(dur), m.Timebase))
	b.WriteString("  <Period id=\"p0\">\n")
	reps := append([]RepConfig(nil), cfg.Reps...)
	sort.SliceStable(reps, func(i, j int) bool { return reps[i].ID < reps[j].ID })
	for _, rep := range reps {
		fmt.Fprintf(&b, "    <AdaptationSet id=%q contentType=\"video\" mimeType=\"video/mp4\">\n", rep.ID)
		fmt.Fprintf(&b,
			"      <SegmentTemplate timescale=%q duration=%q startNumber=\"1\" media=\"$RepresentationID$/seg-$Number$.m4s\" initialization=\"$RepresentationID$/init.mp4\"/>\n",
			fmt.Sprintf("%d", m.Timebase), fmt.Sprintf("%d", DefaultSegmentTicks))
		fmt.Fprintf(&b, "      <Representation id=%q bandwidth=%q/>\n", rep.ID, fmt.Sprintf("%d", rep.Bandwidth))
		b.WriteString("    </AdaptationSet>\n")
	}
	b.WriteString("  </Period>\n</MPD>\n")
	return b.String()
}

func renderHLS(cfg Config, m *Manifest) string {
	rep := cfg.Reps[0]
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", DefaultSegmentTicks/DefaultTimebase)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for i, info := range m.SegmentsByRep[rep.ID] {
		if i == cfg.Segments/2 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXTINF:%d.000,\n%s\n", DefaultSegmentTicks/DefaultTimebase, info.URI)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// isoDuration renders ticks as an ISO-8601 duration with exact seconds.
func isoDuration(ticks, base int64) string {
	whole := ticks / base
	frac := ticks % base
	if frac == 0 {
		return fmt.Sprintf("PT%dS", whole)
	}
	digits := make([]byte, 0, 9)
	n := frac
	for i := 0; i < 9; i++ {
		n *= 10
		digits = append(digits, byte('0'+n/base))
		n %= base
	}
	return fmt.Sprintf("PT%d.%sS", whole, strings.TrimRight(string(digits), "0"))
}

func decodeHex(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(s)
}
