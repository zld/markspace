// Package demo generates self-contained sample subtitle data so the demo
// command never touches external files.
package demo

import (
	"fmt"
	"strings"
	"time"
)

// Fixture is one generated input file: its bytes already carry the declared
// encoding including any BOM.
type Fixture struct {
	Name     string
	Encoding string
	Format   string
	Bytes    []byte
	// Overlaps is the number of overlapping cue pairs embedded by design.
	Overlaps int
}

// SRTFixture builds the acceptance demo: 8 cues with exactly one overlapping
// pair (cue 7 starts before cue 6 ends). The caller re-encodes the returned
// UTF-8 text to UTF-16LE and prepends the BOM.
func SRTFixture() string {
	cues := []struct {
		id       int
		from, to time.Duration
		text     string
	}{
		{1, 0, 2500 * time.Millisecond, "预告片字幕同步演示"},
		{2, 2500 * time.Millisecond, 5 * time.Second, "第二行：多轨道统一管线"},
		{3, 5*time.Second + 120*time.Millisecond, 8 * time.Second, "第三行：有理数帧率换算"},
		{4, 8*time.Second + 40*time.Millisecond, 11*time.Second + 500*time.Millisecond, "第四行：起始帧向下取整"},
		{5, 12 * time.Second, 15 * time.Second, "第五行：结束帧向上取整"},
		{6, 15*time.Second + 250*time.Millisecond, 18*time.Second + 750*time.Millisecond, "第六行：正常结束"},
		// Cue 7 deliberately overlaps cue 6 by 750 ms for the clip policy.
		{7, 18 * time.Second, 21 * time.Second, "第七行：与上一条重叠"},
		{8, 22 * time.Second, 25 * time.Second, "第八行：往返偏差校验"},
	}
	var b strings.Builder
	for _, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			c.id, srtStamp(c.from), srtStamp(c.to), c.text)
	}
	return b.String()
}

// srtStamp renders SRT millisecond timestamps without importing the
// serializer (the demo stays independent of pipeline internals).
func srtStamp(d time.Duration) string {
	total := int64(d)
	h := total / int64(time.Hour)
	total %= int64(time.Hour)
	m := total / int64(time.Minute)
	total %= int64(time.Minute)
	s := total / int64(time.Second)
	ms := (total % int64(time.Second)) * 1000 / int64(time.Second)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}
