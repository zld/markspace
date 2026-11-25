package main

import (
	"bytes"
	"sync"

	"packcheck/internal/clock"
	"packcheck/internal/stream"
	"packcheck/internal/subtitle"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// cutPointSeconds is the shared container cut position in seconds.
func cutPointSeconds() int64 { return 4 }

// cutPoints exposes the cut instants on the unified clock.
func cutPoints() subtitle.CutPoints {
	return subtitle.CutPoints{clock.MustRat(cutPointSeconds(), 1)}
}

// buildSubtitleFixture renders an SRT document whose fourth cue starts at the
// cut point when --split-at-cue is set. It returns the raw byte offset of
// that cue so the report can point at the original position.
func buildSubtitleFixture(opt *options) (doc string, cueOffset int64) {
	var b bytes.Buffer
	cues := []struct {
		idx        int
		start, end string
		text       string
	}{
		{1, "00:00:01,000", "00:00:02,000", "opening"},
		{2, "00:00:02,200", "00:00:03,000", "intro"},
		{3, "00:00:03,200", "00:00:04,000", "pre-cut"},
		{4, "00:00:05,000", "00:00:06,000", "post-cut"},
	}
	if opt.splitAtCue {
		cues[3].start = "00:00:04,000" // lands exactly on the cut point
	}
	for _, c := range cues {
		if c.idx == 4 {
			cueOffset = int64(b.Len())
		}
		b.WriteString(itoa(c.idx) + "\n")
		b.WriteString(c.start + " --> " + c.end + "\n")
		b.WriteString(c.text + "\n\n")
	}
	return b.String(), cueOffset
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// drainerGroup waits for every fan-out consumer goroutine.
type drainerGroup struct {
	wg   sync.WaitGroup
	stop chan struct{}
}

func (g *drainerGroup) wait() {
	close(g.stop)
	g.wg.Wait()
}

// launchDrainers starts one consumer per layer. The slow layer never reads
// its queue, forcing the bounded fan-out to time out and isolate it; every
// other layer drains promptly so its queue peak stays at the capacity. The
// returned stop channel releases the parked slow goroutine at shutdown.
func launchDrainers(fan *stream.FanOut, specs []layerSpec, slow string) *drainerGroup {
	g := &drainerGroup{stop: make(chan struct{})}
	for _, sp := range specs {
		layer := fan.Layer(sp.name)
		g.wg.Add(1)
		go func(l *stream.Layer, isSlow bool) {
			defer g.wg.Done()
			if isSlow {
				// Never consume: wait for shutdown without touching the
				// bounded queue, so the fan-out observes a full queue.
				<-g.stop
				return
			}
			for range l.Drain() {
			}
		}(layer, sp.name == slow)
	}
	return g
}
