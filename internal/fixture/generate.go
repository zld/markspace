package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	pion "github.com/pion/rtp"

	"mediad/internal/adapter/rtp"
)

// Case names are part of the CLI contract and stay stable.
const (
	CaseNormalDASH      = "normal-dash"
	CaseZeroTimescale   = "dash-zero-timescale"
	CaseHLSDiscont      = "hls-discontinuity"
	CaseTruncatedBox    = "truncated-box"
	CaseTimestampFallback = "timestamp-fallback"
)

// Case describes one generated fixture entry for the processing driver.
type Case struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // dash | hls | rtp
	Manifest string `json:"manifest"`
	ExpectOK bool   `json:"expect_ok"`
}

// Index is the top-level cases.json written to the fixture root.
type Index struct {
	Cases []Case `json:"cases"`
}

const (
	manifestTS   uint32 = 1000
	segDuration  int64  = 2000 // ms
	segmentCount        = 3
)

// Generate writes the complete fixture tree under root and returns the index.
func Generate(root string) (*Index, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	idx := &Index{}
	add := func(c Case) { idx.Cases = append(idx.Cases, c) }

	if err := genNormalDASH(root, add); err != nil {
		return nil, err
	}
	if err := genZeroTimescale(root, add); err != nil {
		return nil, err
	}
	if err := genHLS(root, add); err != nil {
		return nil, err
	}
	if err := genTruncatedBox(root, add); err != nil {
		return nil, err
	}
	if err := genTimestampFallback(root, add); err != nil {
		return nil, err
	}
	indexBytes, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "cases.json"), indexBytes, 0o644); err != nil {
		return nil, err
	}
	return idx, nil
}

// videoFrames builds deterministic keyframe-punctuated frames for one
// 2 s segment. A keyframe every second matches the splitter's 1 s target
// duration so the happy path lands every internal boundary exactly.
func videoFrames(segIdx int, timescale uint32) []FrameSpec {
	return []FrameSpec{
		{Payload: []byte(fmt.Sprintf("v-key-%d-0", segIdx)), Duration: timescale, KeyFrame: true},
		{Payload: []byte(fmt.Sprintf("v-key-%d-1", segIdx)), Duration: timescale, KeyFrame: true},
	}
}

func audioFrames(segIdx int) []FrameSpec {
	return []FrameSpec{
		{Payload: silentPCM(64), Duration: 48000, KeyFrame: true},
		{Payload: []byte(fmt.Sprintf("audio-loud-%d", segIdx)), Duration: 48000, KeyFrame: false},
	}
}

// silentPCM returns n bytes at the unsigned 8-bit zero point (128).
func silentPCM(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 128
	}
	return b
}

func writeFile(root, rel string, data []byte) error {
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func genNormalDASH(root string, add func(Case)) error {
	dir := CaseNormalDASH
	for i := 0; i < segmentCount; i++ {
		vseg := BuildSegment(manifestTS, 0, 1,
			uint64(i)*2*uint64(manifestTS), videoFrames(i, manifestTS))
		if err := writeFile(root, filepath.Join(dir, "v", fmt.Sprintf("%d.m4s", i+1)), vseg); err != nil {
			return err
		}
		aseg := BuildSegment(48000, 1, 2,
			uint64(i)*2*48000, audioFrames(i))
		if err := writeFile(root, filepath.Join(dir, "a", fmt.Sprintf("%d.m4s", i+1)), aseg); err != nil {
			return err
		}
	}
	mpd := normalMPD()
	if err := writeFile(root, filepath.Join(dir, "manifest.mpd"), []byte(mpd)); err != nil {
		return err
	}
	add(Case{ID: CaseNormalDASH, Kind: "dash", Manifest: filepath.Join(dir, "manifest.mpd"), ExpectOK: true})
	return nil
}

func normalMPD() string {
	return `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT6S">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v-main">
        <SegmentTemplate timescale="1000" startNumber="1" media="v/$Number$.m4s">
          <SegmentTimeline><S t="0" d="2000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <Representation id="a-main">
        <SegmentTemplate timescale="48000" startNumber="1" media="a/$Number$.m4s">
          <SegmentTimeline><S t="0" d="96000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
}

func genZeroTimescale(root string, add func(Case)) error {
	dir := CaseZeroTimescale
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT6S">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v-bad">
        <SegmentTemplate timescale="0" startNumber="1" media="v/$Number$.m4s">
          <SegmentTimeline><S t="0" d="2000" r="2"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
	if err := writeFile(root, filepath.Join(dir, "manifest.mpd"), []byte(mpd)); err != nil {
		return err
	}
	add(Case{ID: CaseZeroTimescale, Kind: "dash",
		Manifest: filepath.Join(dir, "manifest.mpd"), ExpectOK: false})
	return nil
}

func genHLS(root string, add func(Case)) error {
	dir := CaseHLSDiscont
	for i := 0; i < segmentCount; i++ {
		seg := BuildSegment(1000, 0, 1,
			uint64(i)*2*1000, videoFrames(i, 1000))
		if err := writeFile(root, filepath.Join(dir, "hi", fmt.Sprintf("seg-%d.m4s", i)), seg); err != nil {
			return err
		}
		// Variant lo reuses identical content so cross-representation dedupe
		// has something to merge; boundaries stay inside the 20 ms budget.
		if err := writeFile(root, filepath.Join(dir, "lo", fmt.Sprintf("seg-%d.m4s", i)), seg); err != nil {
			return err
		}
	}
	master := "#EXTM3U\n#EXT-X-VERSION:3\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000\nhi/index.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=500000\nlo/index.m3u8\n"
	if err := writeFile(root, filepath.Join(dir, "master.m3u8"), []byte(master)); err != nil {
		return err
	}
	media := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:2.000,\nseg-0.m4s\n" +
		"#EXT-X-DISCONTINUITY\n#EXTINF:2.000,\nseg-1.m4s\n" +
		"#EXTINF:2.000,\nseg-2.m4s\n#EXT-X-ENDLIST\n"
	for _, v := range []string{"hi", "lo"} {
		if err := writeFile(root, filepath.Join(dir, v, "index.m3u8"), []byte(media)); err != nil {
			return err
		}
	}
	add(Case{ID: CaseHLSDiscont, Kind: "hls",
		Manifest: filepath.Join(dir, "master.m3u8"), ExpectOK: true})
	return nil
}

func genTruncatedBox(root string, add func(Case)) error {
	dir := CaseTruncatedBox
	good := BuildSegment(1000, 0, 1, 0, videoFrames(0, 1000))
	// Inflate the mdat size header past EOF: structurally present but corrupt.
	broken := append([]byte(nil), good...)
	frameBytes := len(videoFrames(0, 1000)[0].Payload) + len(videoFrames(0, 1000)[1].Payload)
	mdatHdr := len(broken) - 8 - frameBytes
	putBE32(broken[mdatHdr:], readBE32(broken[mdatHdr:])+16)
	if err := writeFile(root, filepath.Join(dir, "seg.m4s"), broken); err != nil {
		return err
	}
	mpd := singleSegMPD(CaseTruncatedBox, "seg.m4s")
	if err := writeFile(root, filepath.Join(dir, "manifest.mpd"), []byte(mpd)); err != nil {
		return err
	}
	add(Case{ID: CaseTruncatedBox, Kind: "dash",
		Manifest: filepath.Join(dir, "manifest.mpd"), ExpectOK: false})
	return nil
}

func genTimestampFallback(root string, add func(Case)) error {
	dir := CaseTimestampFallback
	// RTP capture whose timestamp steps backwards after the first packet.
	pkts := []*pion.Packet{
		Packet(96, 0, 1000, 7, false, []byte("p0")),
		Packet(96, 1, 400, 7, false, []byte("p1")), // 600-tick fallback
		Packet(96, 2, 560, 7, false, []byte("p2")),
	}
	capBytes, err := WriteCapture(pkts)
	if err != nil {
		return err
	}
	if err := writeFile(root, filepath.Join(dir, "stream.rtp"), capBytes); err != nil {
		return err
	}
	meta, err := rtp.EncodeMeta(rtp.StreamMeta{
		ClockRate: 8000, Kind: "audio", FrameTicks: 160,
	})
	if err != nil {
		return err
	}
	if err := writeFile(root, filepath.Join(dir, "stream.meta.json"), meta); err != nil {
		return err
	}
	add(Case{ID: CaseTimestampFallback, Kind: "rtp",
		Manifest: filepath.Join(dir, "stream.rtp"), ExpectOK: false})
	return nil
}

func singleSegMPD(dir, url string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT2S">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v-%s">
        <SegmentTemplate timescale="1000" startNumber="1" media="%s">
          <SegmentTimeline><S t="0" d="2000"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`, dir, url)
}

func readBE32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func putBE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
