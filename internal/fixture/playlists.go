package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// WriteHLSPlaylist writes one master document containing a separated
// media playlist section per track, matching the parser's fixture subset.
func WriteHLSPlaylist(fx *Fixture, path string) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString(fmt.Sprintf("#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n", SegmentSeconds))
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, spec := range fx.Tracks {
		dir := trackDirName(spec.ID)
		b.WriteString(fmt.Sprintf(
			"#EXT-X-FIXTURE-PLAYLIST:%s,bandwidth=%d,width=%d,height=%d\n",
			spec.Name, spec.TargetBitrateKbps*1000, spec.Width, spec.Height))
		b.WriteString("#EXT-X-MEDIA-SEQUENCE:1\n")
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
		for seq := uint32(1); seq <= SegmentCount; seq++ {
			b.WriteString(fmt.Sprintf("#EXTINF:%d.000,\n", SegmentSeconds))
			b.WriteString(fmt.Sprintf("%s/seg-%d.m4s\n", dir, seq))
		}
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return merr.Wrap(err, "fixture", path, "HLS playlist write failed")
	}
	return nil
}

// WriteDASHPlaylist writes a single-Period MPD with one Representation
// per track and an explicit SegmentList (timescale/duration/startNumber).
func WriteDASHPlaylist(fx *Fixture, path string) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(fmt.Sprintf(
		`<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" minBufferTime="PT%dS">`+"\n",
		SegmentSeconds))
	b.WriteString(`  <Period id="P0">` + "\n")
	b.WriteString(`    <AdaptationSet id="video" contentType="video" segmentAlignment="true">` + "\n")
	for _, spec := range fx.Tracks {
		b.WriteString(writeRepresentation(spec))
	}
	b.WriteString("    </AdaptationSet>\n  </Period>\n</MPD>\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return merr.Wrap(err, "fixture", path, "DASH playlist write failed")
	}
	return nil
}

func writeRepresentation(spec media.TrackSpec) string {
	var b strings.Builder
	indent := "      "
	b.WriteString(fmt.Sprintf(
		indent+`<Representation id="%s" bandwidth="%d" width="%d" height="%d">`+"\n",
		spec.Name, spec.TargetBitrateKbps*1000, spec.Width, spec.Height))
	b.WriteString(fmt.Sprintf(
		indent+`  <SegmentList timescale="%d" duration="%d" startNumber="1">`+"\n",
		media.ClockRate, media.ClockRate*SegmentSeconds))
	dir := trackDirName(spec.ID)
	for seq := uint32(1); seq <= SegmentCount; seq++ {
		b.WriteString(fmt.Sprintf(indent+`    <SegmentURL media="%s/seg-%d.m4s"/>`+"\n",
			dir, seq))
	}
	b.WriteString(indent + "  </SegmentList>\n")
	b.WriteString(indent + "</Representation>\n")
	return b.String()
}

// FixtureRoot helpers used by the ingest layer.
const (
	// HLSPath / DASHPath return manifest locations inside a fixture root.
	HLSManifest  = "hls/index.m3u8"
	DASHManifest = "dash/manifest.mpd"
	MetaFile     = "fixture.json"
)

// AbsJoin safely joins a fixture root with a relative component,
// rejecting traversal attempts at the boundary.
func AbsJoin(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", merr.New(merr.KindArgument, "fixture", rel,
			"absolute path inside fixture is not allowed")
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return "", merr.New(merr.KindArgument, "fixture", rel,
				"path traversal is not allowed")
		}
	}
	return filepath.Join(root, clean), nil
}
