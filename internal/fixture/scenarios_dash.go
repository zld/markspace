package fixture

import (
	"fmt"
	"path/filepath"
)

func genDASH(dir string, gt *GroundTruth) error {
	const segs, samples = 3, 30
	// v0: 90k timescale, 3000 delta. v1: 45k timescale, 1500 delta; both 1s/seg.
	for _, rep := range []struct {
		id    string
		ts    uint32
		delta uint32
	}{{"v0", 90000, 3000}, {"v1", 45000, 1500}} {
		for s := 1; s <= segs; s++ {
			rel := fmt.Sprintf("dash/%s/seg-%d.mp4", rep.id, s)
			deltas := make([]uint32, samples)
			for i := range deltas {
				deltas[i] = rep.delta
			}
			if _, err := writeProgressive(filepath.Join(dir, rel), rep.ts, deltas, 2048, 15, uint64(500+s)); err != nil {
				return err
			}
		}
	}

	aligned := `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v0" bandwidth="800000">
        <SegmentTemplate timescale="90000" startNumber="1" media="dash/v0/seg-$Number$.mp4">
          <SegmentTimeline><S t="0" d="90000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v1" bandwidth="1600000">
        <SegmentTemplate timescale="45000" startNumber="1" media="dash/v1/seg-$Number$.mp4">
          <SegmentTimeline><S t="0" d="45000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
	gt.DashManifest = filepath.Join(dir, "multi.mpd")
	if err := writeBytes(gt.DashManifest, []byte(aligned)); err != nil {
		return err
	}

	misaligned := `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v0" bandwidth="800000">
        <SegmentTemplate timescale="90000" startNumber="1" media="dash/v0/seg-$Number$.mp4">
          <SegmentTimeline><S t="0" d="90000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v1" bandwidth="1600000">
        <SegmentTemplate timescale="45000" startNumber="1" media="dash/v1/seg-$Number$.mp4">
          <SegmentTimeline><S t="9000" d="45000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
	gt.DashMisalignedManifest = filepath.Join(dir, "misaligned.mpd")
	return writeBytes(gt.DashMisalignedManifest, []byte(misaligned))
}
