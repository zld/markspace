package fixture

import (
	"os"
	"path/filepath"
)

func generateTruncated(root string) (ManifestCase, error) {
	dir := filepath.Join(root, "truncated")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ManifestCase{}, err
	}
	if err := WriteInitMP4(filepath.Join(dir, "init.mp4"), 90000); err != nil {
		return ManifestCase{}, err
	}
	info, err := BuildFragment(FragmentSpec{
		Sequence: 1,
		Samples:  []SampleSpec{{Duration: 1800, Size: 32}, {Duration: 1800, Size: 32}},
	})
	if err != nil {
		return ManifestCase{}, err
	}
	cut := info.SampleOffsets[1] + 8
	if err := os.WriteFile(filepath.Join(dir, "corrupt.m4s"), info.Bytes[:cut], 0o644); err != nil {
		return ManifestCase{}, err
	}
	mpd := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="trunc">
<SegmentTemplate timescale="90000" initialization="init.mp4" media="corrupt.m4s" startNumber="1">
<SegmentTimeline><S t="0" d="3600"/></SegmentTimeline>
</SegmentTemplate></Representation></AdaptationSet></Period></MPD>
`
	mpdPath := filepath.Join(dir, "truncated.mpd")
	if err := writeFile(mpdPath, mpd); err != nil {
		return ManifestCase{}, err
	}
	return ManifestCase{
		Name: "truncated-shard", Kind: "truncated", Manifest: mpdPath,
		Representation: "trunc", EntryIndex: 0, ByteOffset: cut, ExpectedExitCode: 2,
	}, nil
}

func generateIllegal(root string) (ManifestCase, error) {
	dir := filepath.Join(root, "illegal-sample")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ManifestCase{}, err
	}
	if err := WriteInitMP4(filepath.Join(dir, "init.mp4"), 90000); err != nil {
		return ManifestCase{}, err
	}
	info, err := WriteFragment(filepath.Join(dir, "illegal.m4s"), FragmentSpec{
		Sequence: 1,
		Samples: []SampleSpec{
			{Duration: 1800, Size: 24},
			{Duration: 1800, Size: 0},
			{Duration: 1800, Size: 24},
		},
	})
	if err != nil {
		return ManifestCase{}, err
	}
	mpd := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="illegal">
<SegmentTemplate timescale="90000" initialization="init.mp4" media="illegal.m4s" startNumber="1">
<SegmentTimeline><S t="0" d="5400"/></SegmentTimeline>
</SegmentTemplate></Representation></AdaptationSet></Period></MPD>
`
	mpdPath := filepath.Join(dir, "illegal.mpd")
	if err := writeFile(mpdPath, mpd); err != nil {
		return ManifestCase{}, err
	}
	return ManifestCase{
		Name: "illegal-sample-length", Kind: "illegal-sample", Manifest: mpdPath,
		Representation: "illegal", EntryIndex: 0, ByteOffset: info.SampleOffsets[1], ExpectedExitCode: 2,
	}, nil
}

func generateZeroTimescale(root string) (string, error) {
	dir := filepath.Join(root, "zero-timescale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	mpdPath := filepath.Join(dir, "zero.mpd")
	mpd := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="zero">
<SegmentTemplate timescale="0" initialization="init.mp4" media="seg-$Number$.m4s" startNumber="1">
<SegmentTimeline><S t="0" d="3600"/></SegmentTimeline>
</SegmentTemplate></Representation></AdaptationSet></Period></MPD>
`
	return mpdPath, writeFile(mpdPath, mpd)
}

func generateRegressionHLS(root string) (string, error) {
	dir := filepath.Join(root, "time-regression")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "backwards.m3u8")
	playlist := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA-SEQUENCE:20
#EXT-X-PROGRAM-DATE-TIME:2024-01-01T00:00:02Z
#EXTINF:0.040,
first.m4s
#EXT-X-PROGRAM-DATE-TIME:2024-01-01T00:00:01Z
#EXTINF:0.040,
second.m4s
#EXT-X-ENDLIST
`
	return path, writeFile(path, playlist)
}
