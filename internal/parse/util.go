package parse

import "strconv"

// itoa is a short local wrapper around the standard integer formatter.
func itoa(i int) string { return strconv.Itoa(i) }

// itoa64 formats int64 values used by frame numbers.
func itoa64(i int64) string { return strconv.FormatInt(i, 10) }
