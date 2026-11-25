package fixture

import (
	"fmt"
	"os"
	"path/filepath"
)

// ManifestCase carries expected located-error coordinates for a negative fixture.
type ManifestCase struct {
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	Manifest         string `json:"manifest"`
	Representation   string `json:"representation,omitempty"`
	EntryIndex       int    `json:"entry_index"`
	ByteOffset       int64  `json:"byte_offset"`
	ExpectedExitCode int    `json:"expected_exit_code"`
}

// Bundle is the deterministic set of generated local fixtures.
type Bundle struct {
	WorkRoot       string         `json:"work_root"`
	NormalDASH     string         `json:"normal_dash"`
	NormalHLS      string         `json:"normal_hls"`
	MisalignedDASH string         `json:"misaligned_dash"`
	Truncated      ManifestCase   `json:"truncated"`
	IllegalSample  ManifestCase   `json:"illegal_sample"`
	ZeroTimescale  string         `json:"zero_timescale"`
	RegressionHLS  string         `json:"regression_hls"`
	Cases          []ManifestCase `json:"cases"`
}

// GenerateAll creates normal and negative fixtures without external media.
func GenerateAll(root string) (*Bundle, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	b := &Bundle{WorkRoot: root}
	dash, hls, mis, err := generateNormal(root)
	if err != nil {
		return nil, err
	}
	b.NormalDASH = dash
	b.NormalHLS = hls
	b.MisalignedDASH = mis

	trunc, err := generateTruncated(root)
	if err != nil {
		return nil, err
	}
	b.Truncated = trunc
	b.Cases = append(b.Cases, trunc)

	illegal, err := generateIllegal(root)
	if err != nil {
		return nil, err
	}
	b.IllegalSample = illegal
	b.Cases = append(b.Cases, illegal)

	zero, err := generateZeroTimescale(root)
	if err != nil {
		return nil, err
	}
	b.ZeroTimescale = zero
	b.Cases = append(b.Cases, ManifestCase{
		Name: "zero-timescale", Kind: "zero-timescale", Manifest: zero,
		Representation: "zero", EntryIndex: 0, ExpectedExitCode: 2,
	})

	reg, err := generateRegressionHLS(root)
	if err != nil {
		return nil, err
	}
	b.RegressionHLS = reg
	b.Cases = append(b.Cases, ManifestCase{
		Name: "time-regression", Kind: "time-regression", Manifest: reg,
		EntryIndex: 1, ExpectedExitCode: 2,
	})
	return b, nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func generateNormal(root string) (dashPath, hlsPath, misPath string, err error) {
	dir := filepath.Join(root, "normal")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", "", err
	}
	if err = WriteInitMP4(filepath.Join(dir, "init-video.mp4"), 90000); err != nil {
		return
	}
	if err = WriteInitMP4(filepath.Join(dir, "init-audio.mp4"), 48000); err != nil {
		return
	}
	if err = WriteInitMP4(filepath.Join(dir, "init-wide.mp4"), 1_000_000_000); err != nil {
		return
	}
	videoSamples := []SampleSpec{{Duration: 1800, Size: 24}, {Duration: 1800, Size: 32}}
	audioSamples := []SampleSpec{{Duration: 960, Size: 16}, {Duration: 960, Size: 18}}
	wideSamples := []SampleSpec{{Duration: 20_000_000, Size: 20}, {Duration: 20_000_000, Size: 22}}
	for i, base := range []uint64{0, 3600} {
		n := uint32(i + 1)
		if _, err = WriteFragment(filepath.Join(dir, fmt.Sprintf("seg-video-%d.m4s", n)), FragmentSpec{Sequence: n, BaseDecodeTime: base, Samples: videoSamples}); err != nil {
			return
		}
		if _, err = WriteFragment(filepath.Join(dir, fmt.Sprintf("seg-audio-%d.m4s", n)), FragmentSpec{Sequence: n, BaseDecodeTime: uint64(i * 1920), Samples: audioSamples}); err != nil {
			return
		}
	}
	for i, base := range []uint64{25_000_000, 65_000_000} {
		n := uint32(i + 1)
		if _, err = WriteFragment(filepath.Join(dir, fmt.Sprintf("seg-wide-%d.m4s", n)), FragmentSpec{Sequence: n, BaseDecodeTime: base, Samples: wideSamples}); err != nil {
			return
		}
	}

	dashPath = filepath.Join(dir, "manifest.mpd")
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period id="0">
    <AdaptationSet contentType="av">
      <Representation id="video" bandwidth="500000">
        <SegmentTemplate timescale="90000" initialization="init-video.mp4" media="seg-video-$Number$.m4s" startNumber="1">
          <SegmentTimeline><S t="0" d="3600"/><S t="3600" d="3600"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="audio" bandwidth="128000">
        <SegmentTemplate timescale="48000" initialization="init-audio.mp4" media="seg-audio-$Number$.m4s" startNumber="1">
          <SegmentTimeline><S t="0" d="1920"/><S t="1920" d="1920"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="wide" bandwidth="300000">
        <SegmentTemplate timescale="1000000000" initialization="init-wide.mp4" media="seg-wide-$Number$.m4s" startNumber="1">
          <SegmentTimeline><S t="25000000" d="40000000"/><S t="65000000" d="40000000"/></SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`
	if err = writeFile(dashPath, mpd); err != nil {
		return
	}

	hlsDir := filepath.Join(root, "hls")
	if err = os.MkdirAll(hlsDir, 0o755); err != nil {
		return
	}
	if err = WriteInitMP4(filepath.Join(hlsDir, "init-hls.mp4"), 90000); err != nil {
		return
	}
	for _, n := range []uint32{10, 11} {
		if _, err = WriteFragment(filepath.Join(hlsDir, fmt.Sprintf("hls-%d.m4s", n)), FragmentSpec{
			Sequence: n, BaseDecodeTime: 0, Samples: videoSamples,
		}); err != nil {
			return
		}
	}
	hlsPath = filepath.Join(hlsDir, "playlist.m3u8")
	playlist := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:1
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="init-hls.mp4"
#EXTINF:0.040,
hls-10.m4s
#EXT-X-DISCONTINUITY
#EXTINF:0.040,
hls-11.m4s
#EXT-X-ENDLIST
`
	if err = writeFile(hlsPath, playlist); err != nil {
		return
	}

	misDir := filepath.Join(root, "misaligned")
	if err = os.MkdirAll(misDir, 0o755); err != nil {
		return
	}
	if err = WriteInitMP4(filepath.Join(misDir, "init.mp4"), 1_000_000_000); err != nil {
		return
	}
	if _, err = WriteFragment(filepath.Join(misDir, "seg-1.m4s"), FragmentSpec{Sequence: 1, Samples: wideSamples}); err != nil {
		return
	}
	misPath = filepath.Join(misDir, "misaligned.mpd")
	misMPD := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="drift">
<SegmentTemplate timescale="1000000000" initialization="init.mp4" media="seg-$Number$.m4s" startNumber="1">
<SegmentTimeline><S t="25000000" d="40000000"/></SegmentTimeline>
</SegmentTemplate></Representation></AdaptationSet></Period></MPD>
`
	err = writeFile(misPath, misMPD)
	return
}
