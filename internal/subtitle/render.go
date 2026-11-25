package subtitle

import (
	"strconv"

	"mediapack/internal/clock"
)

func itoa(n int) string { return strconv.Itoa(n) }

func stampParts(t clock.Ticks) (h, m, s, ms int64) {
	totalMS := int64(t) * 1000 / clock.TicksPerSecond
	ms = totalMS % 1000
	totalSec := totalMS / 1000
	s = totalSec % 60
	m = (totalSec / 60) % 60
	h = totalSec / 3600
	return
}

func formatSRTStamp(t clock.Ticks) string {
	h, m, s, ms := stampParts(t)
	return pad2(h) + ":" + pad2(m) + ":" + pad2(s) + "," + pad3(ms)
}

func formatVTTStamp(t clock.Ticks) string {
	h, m, s, ms := stampParts(t)
	return pad2(h) + ":" + pad2(m) + ":" + pad2(s) + "." + pad3(ms)
}

func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

func pad3(v int64) string {
	switch {
	case v < 10:
		return "00" + strconv.FormatInt(v, 10)
	case v < 100:
		return "0" + strconv.FormatInt(v, 10)
	default:
		return strconv.FormatInt(v, 10)
	}
}
