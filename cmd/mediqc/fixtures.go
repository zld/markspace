package main

import (
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/abema/go-mp4"

	"mediqc/internal/clock"
)

// fixture holds the deterministic synthetic assets for one seed.
type fixture struct {
	SRT string
	VTT string
	// Representations: name, rate, keyframe frames, total frames.
	Reps []repFixture
}

type repFixture struct {
	Name       string
	Rate       clock.Rate
	Drop       bool
	Keyframes  []int64
	TotalFrame int64
}

const (
	fixtureDurationTicks = 30 * clock.Timescale // 30s asset
	fixtureSegTargetTick = 2 * clock.Timescale  // 2s segment target
)

// gopKeyframes builds keyframes every gop frames up to total.
func gopKeyframes(gop, total int64) []int64 {
	var k []int64
	for f := int64(0); f < total; f += gop {
		k = append(k, f)
	}
	return k
}

// makeFixture derives all assets deterministically from seed.
func makeFixture(seed int64) *fixture {
	rng := rand.New(rand.NewSource(seed))
	var srt, vtt strings.Builder
	vtt.WriteString("WEBVTT\n\n")
	cues := 24
	for i := 0; i < cues; i++ {
		startMs := int64(i)*1200 + int64(rng.Intn(200))
		endMs := startMs + 700 + int64(rng.Intn(300))
		text := fmt.Sprintf("program cue %d seed %d", i+1, seed)
		fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n",
			i+1, srtTS(startMs), srtTS(endMs), text)
		fmt.Fprintf(&vtt, "%s --> %s\n%s\n\n", vttTS(startMs), vttTS(endMs), text)
	}
	reps := []repFixture{
		{Name: "v24", Rate: clock.FPS24, Keyframes: gopKeyframes(48, 720), TotalFrame: 720},
		{Name: "v25", Rate: clock.FPS25, Keyframes: gopKeyframes(50, 750), TotalFrame: 750},
		{Name: "v50", Rate: clock.FPS50, Keyframes: gopKeyframes(100, 1500), TotalFrame: 1500},
	}
	return &fixture{SRT: srt.String(), VTT: vtt.String(), Reps: reps}
}

func srtTS(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func vttTS(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// mp4Mode selects how sync-sample info is stored.
type mp4Mode int

const (
	mp4Index mp4Mode = iota // stss index present
	mp4Scan                 // no index; trun sample flags only
	mp4None                 // neither (corrupt fixture)
)

// writeMP4 writes a minimal MP4 with the given keyframes either as an
// stss index or as trun sample flags (index missing -> scan path).
func writeMP4(path string, keyframes []int64, totalFrames int64, mode mp4Mode) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)
	start := func(t mp4.BoxType) error {
		_, err := w.StartBox(&mp4.BoxInfo{Type: t})
		return err
	}
	end := func() error {
		_, err := w.EndBox()
		return err
	}
	for _, t := range []mp4.BoxType{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(),
		mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl()} {
		if err := start(t); err != nil {
			return err
		}
	}
	if mode == mp4Index {
		nums := make([]uint32, len(keyframes))
		for i, k := range keyframes {
			nums[i] = uint32(k + 1)
		}
		if err := start(mp4.BoxTypeStss()); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Stss{
			EntryCount: uint32(len(nums)), SampleNumber: nums,
		}, mp4.Context{}); err != nil {
			return err
		}
		if err := end(); err != nil {
			return err
		}
	}
	for i := 0; i < 5; i++ {
		if err := end(); err != nil {
			return err
		}
	}
	if mode != mp4Scan {
		return nil
	}
	// No index: carry sync info in trun sample flags instead.
	kf := map[int64]bool{}
	for _, k := range keyframes {
		kf[k] = true
	}
	if err := start(mp4.BoxTypeMoof()); err != nil {
		return err
	}
	if err := start(mp4.BoxTypeTraf()); err != nil {
		return err
	}
	trun := &mp4.Trun{
		FullBox:     mp4.FullBox{Version: 0, Flags: [3]byte{0x00, 0x04, 0x00}},
		SampleCount: uint32(totalFrames),
	}
	for i := int64(0); i < totalFrames; i++ {
		var flags uint32
		if !kf[i] {
			flags = 0x10000 // non-sync
		}
		trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleFlags: flags})
	}
	if err := start(mp4.BoxTypeTrun()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, trun, mp4.Context{}); err != nil {
		return err
	}
	if err := end(); err != nil {
		return err
	}
	if err := end(); err != nil {
		return err
	}
	return end()
}
