package fixture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
)

// GenerateCorrupt writes the boundary fixtures, each in its own
// self-contained subdirectory under dir/corrupt:
//   - truncated: media segment ending in a truncated box header
//   - zerots:    MPD with an illegal zero timescale
//   - dupts:     zero-duration samples with duplicate timestamps
func GenerateCorrupt(dir string) error {
	if err := corruptTruncated(filepath.Join(dir, "corrupt", "truncated")); err != nil {
		return err
	}
	if err := corruptZeroTimescale(filepath.Join(dir, "corrupt", "zerots")); err != nil {
		return err
	}
	return corruptDuplicateTS(filepath.Join(dir, "corrupt", "dupts"))
}

// singleRepMPD writes a one-representation MPD with nseg segments.
func singleRepMPD(dir, timescale string, nseg int) error {
	var entries bytes.Buffer
	for i := 0; i < nseg; i++ {
		entries.WriteString(`            <S d="180000" r="0"/>` + "\n")
	}
	mpd := fmt.Sprintf(`<?xml version="1.0"?>
<MPD>
  <Period>
    <AdaptationSet>
      <Representation id="c1" bandwidth="1200000">
        <SegmentTemplate timescale="%s" initialization="init-c1.mp4"
            media="seg-c1-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
%s          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`, timescale, entries.String())
	return os.WriteFile(filepath.Join(dir, "manifest.mpd"), []byte(mpd), 0o644)
}

func writeInitC1(dir string) error {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(Timescale, "video", "und")
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "init-c1.mp4"), buf.Bytes(), 0o644)
}

// corruptTruncated: a segment whose file ends with half a box header.
func corruptTruncated(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := singleRepMPD(dir, "90000", 1); err != nil {
		return err
	}
	if err := writeInitC1(dir); err != nil {
		return err
	}
	good := EncodeSegment(1, 0, 5000)
	truncated := append(good, 0x00, 0x00, 0x10, 0x00) // 4 bytes: half a box header
	return os.WriteFile(filepath.Join(dir, "seg-c1-1.m4s"), truncated, 0o644)
}

// corruptZeroTimescale: an MPD declaring timescale="0".
func corruptZeroTimescale(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := singleRepMPD(dir, "0", 1); err != nil {
		return err
	}
	if err := writeInitC1(dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "seg-c1-1.m4s"), EncodeSegment(1, 0, 5000), 0o644)
}

// corruptDuplicateTS: zero-duration samples whose timestamps cannot be
// made monotonic, so repair must stop the track.
func corruptDuplicateTS(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := singleRepMPD(dir, "90000", 1); err != nil {
		return err
	}
	if err := writeInitC1(dir); err != nil {
		return err
	}
	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		return err
	}
	for i := 0; i < 10; i++ {
		frag.AddFullSample(mp4.FullSample{
			Sample:     mp4.NewSample(mp4.SyncSampleFlags, 0, 100, 0), // zero duration
			DecodeTime: 0,                                            // all duplicate
			Data:       bytes.Repeat([]byte{byte(i)}, 100),
		})
	}
	var buf bytes.Buffer
	if err := frag.Encode(&buf); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "seg-c1-1.m4s"), buf.Bytes(), 0o644)
}
