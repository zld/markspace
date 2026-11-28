package stage

import "time"

// nowMS returns wall-clock milliseconds. Tests inject a fake clock instead.
func nowMS() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
