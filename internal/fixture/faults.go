package fixture

import (
	"fmt"
	"path/filepath"
	"strings"

	"continuity/internal/manifest"
	"continuity/internal/media"
)

func buildDamaged(s *Spec) error {
	s.OOBReference = "../../outside-escape.m4s"
	oob := filepath.Join(s.Root, "oob.m3u8")
	pl := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:900\n#EXTINF:1.0,\n" + s.OOBReference + "\n"
	if err := write(oob, pl); err != nil {
		return err
	}
	s.Entry = oob
	s.Format = "hls"

	s.TruncatedFile = filepath.Join(s.Root, "media", "truncated-video.m4s")
	if err := WriteFragment(s.TruncatedFile, FragmentSpec{
		Sequence: 900, TrackID: 1, BaseTicks: 810000,
		Samples:       []MP4Sample{{Duration: 45000, Size: 24}, {Duration: 45000, Size: 128}},
		ActualPayload: patternBytes(32, 0xEE),
	}); err != nil {
		return err
	}
	_, err := media.ReadFragment(s.TruncatedFile, 90000)
	f, ok := media.AsFault(err)
	if !ok || f.Code != media.CodeTruncatedMP4 {
		return fmt.Errorf("fixture self-check failed for truncated MP4: %w", err)
	}
	s.TruncationOffset = f.Offset

	// Ensure the manifest parser also recognizes the generated traversal.
	if _, err := manifest.ParseHLSPlaylist(oob, s.Root, "video"); err == nil {
		return fmt.Errorf("fixture self-check failed: OOB reference was accepted")
	}
	return nil
}

func buildBoundary(s *Spec) error {
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD type="static" xmlns="urn:mpeg:dash:schema:mpd:2011">
  <Period id="p0">
    <AdaptationSet contentType="video">
      <Representation id="video-boundary" bandwidth="1000000">
        <SegmentTemplate timescale="1" media="boundary-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="18446744073709551615"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
	s.OverflowFile = filepath.Join(s.Root, "boundary-overflow.mpd")
	if err := write(s.OverflowFile, mpd); err != nil {
		return err
	}
	s.Entry = s.OverflowFile
	s.Format = "dash"
	if i := strings.Index(mpd, `d="18446744073709551615"`); i >= 0 {
		s.OverflowOffset = int64(i)
	}
	if _, err := manifest.ParseDASH(s.OverflowFile, s.Root); err == nil {
		return fmt.Errorf("fixture self-check failed: overflow DASH was accepted")
	} else if f, ok := media.AsFault(err); !ok || f.Code != media.CodeTimeOverflow {
		return fmt.Errorf("fixture self-check got %v, want time overflow", err)
	}

	rtpFix, err := buildBoundaryRTP(s.Root)
	if err != nil {
		return err
	}
	s.RTPPath = rtpFix.Path
	s.Recovered = rtpFix.Recovered
	s.Lost = rtpFix.Lost
	s.Unrecoverable = rtpFix.Unrecoverable
	return nil
}
