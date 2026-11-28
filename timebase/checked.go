package timebase

import (
	"fmt"
	"math"

	"mediarc/model"
)

func checkedMul(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a < 0 || b < 0 {
		return 0, model.New(model.CodeNegativeTime, stream, "negative component in timecode arithmetic")
	}
	if a > math.MaxInt64/b {
		return 0, model.New(model.CodeTimestampOverflow, stream,
			fmt.Sprintf("%d * %d overflows int64", a, b))
	}
	return a * b, nil
}

func checkedAdd(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, model.New(model.CodeNegativeTime, stream, "negative component in timecode arithmetic")
	}
	if a > math.MaxInt64-b {
		return 0, model.New(model.CodeTimestampOverflow, stream,
			fmt.Sprintf("%d + %d overflows int64", a, b))
	}
	return a + b, nil
}

func checkedSub(a, b int64) (int64, error) {
	if b < 0 || a < b {
		return 0, model.New(model.CodeNegativeTime, stream, "negative component in timecode arithmetic")
	}
	return a - b, nil
}
