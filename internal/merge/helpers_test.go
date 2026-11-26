package merge

import "mediamerge/internal/clock"

func clockTicks(sec int64) clock.Ticks {
	t, err := clock.FromSeconds(sec)
	if err != nil {
		panic(err)
	}
	return t
}
