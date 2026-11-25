package demo

import (
	"encoding/binary"
	"path/filepath"
)

// generateCorrupt writes the damaged fixture set: a truncated MP4, an
// out-of-range sample table, a bad keyframe index, and unparseable
// manifest fragments. Each records the first locatable bad byte offset so
// the CLI can assert errors are positioned rather than generic.
func (b *Bundle) generateCorrupt() error {
	base := mp4Segment(1, 90000, 4, 16, 2)

	// 1) Truncated: keep ftyp and a moov header that promises more bytes
	// than remain. The first bad offset is the moov header start.
	moovType := indexTag(base, "moov")
	moovOff := moovType - 4
	truncated := append([]byte(nil), base[:moovOff+8]...)
	binary.BigEndian.PutUint32(truncated[moovOff:moovOff+4], 500)
	truncPath := filepath.Join(b.Root, DirCorrupt, "truncated.m4s")
	if err := writeFile(truncPath, truncated); err != nil {
		return err
	}
	b.Corrupt = append(b.Corrupt, CorruptFixture{
		Path: truncPath, BadOffset: int64(moovOff), Kind: "truncated",
	})

	// 2) Illegal range: point the stco chunk past EOF.
	oor := append([]byte(nil), base...)
	stcoType := indexTag(oor, "stco")
	binary.BigEndian.PutUint32(oor[stcoType+12:stcoType+16], uint32(len(oor)+100))
	oorPath := filepath.Join(b.Root, DirCorrupt, "out-of-range.m4s")
	if err := writeFile(oorPath, oor); err != nil {
		return err
	}
	b.Corrupt = append(b.Corrupt, CorruptFixture{
		Path: oorPath, BadOffset: int64(stcoType + 12), Kind: "illegal-range",
	})

	// 3) Bad index: stss references a sample beyond stsz sample count.
	badIdx := append([]byte(nil), base...)
	stssType := indexTag(badIdx, "stss")
	// stss type + fullbox(4) + count(4) then first sample number.
	binary.BigEndian.PutUint32(badIdx[stssType+8:stssType+12], 999)
	idxPath := filepath.Join(b.Root, DirCorrupt, "bad-index.m4s")
	if err := writeFile(idxPath, badIdx); err != nil {
		return err
	}
	b.Corrupt = append(b.Corrupt, CorruptFixture{
		Path: idxPath, BadOffset: int64(stssType + 8), Kind: "index-missing",
	})

	// 4) Malformed manifest: an HLS EXTINF missing its duration value.
	badPlaylist := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:\nseg.ts\n")
	plPath := filepath.Join(b.Root, DirCorrupt, "bad-playlist.m3u8")
	if err := writeFile(plPath, badPlaylist); err != nil {
		return err
	}
	// The bad EXTINF line starts after the first two lines.
	badOff := int64(len("#EXTM3U\n#EXT-X-TARGETDURATION:6\n"))
	b.Corrupt = append(b.Corrupt, CorruptFixture{
		Path: plPath, BadOffset: badOff, Kind: "manifest",
	})

	// 5) Malformed MPD: duplicate segment time in a SegmentTimeline.
	badMPD := []byte(`<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="x">
  <SegmentTemplate timescale="90000" startNumber="1"><SegmentTimeline>
    <S t="0" d="90000"/>
    <S t="90000" d="90000"/>
    <S t="90000" d="45000"/>
  </SegmentTimeline></SegmentTemplate>
</Representation></AdaptationSet></Period></MPD>`)
	mpdPath := filepath.Join(b.Root, DirCorrupt, "bad-manifest.mpd")
	if err := writeFile(mpdPath, badMPD); err != nil {
		return err
	}
	marker := []byte(`<S t="90000" d="45000"`)
	badOff = int64(indexBytes(badMPD, marker))
	b.Corrupt = append(b.Corrupt, CorruptFixture{
		Path: mpdPath, BadOffset: badOff, Kind: "manifest",
	})
	return nil
}

// indexBytes returns the first offset of marker within raw, or -1.
func indexBytes(raw, marker []byte) int {
	for i := 0; i+len(marker) <= len(raw); i++ {
		if string(raw[i:i+len(marker)]) == string(marker) {
			return i
		}
	}
	return -1
}
