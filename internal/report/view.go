package report

import (
	"encoding/hex"
	"fmt"

	rtpx "rtspcheck/internal/rtp"
	"rtspcheck/internal/session"
)

// ResultView is the subset of a transport result the report needs.
// Defining it here keeps the report package free of transport imports
// and makes expectation tests trivial to construct.
type ResultView interface {
	Failure() error
	FailureStage() session.State
	FailureMethod() session.Method
	Transitions() []session.Transition
	Gaps() []rtpx.Gap
	GapCount() int
	Frames() []rtpx.Frame
	FrameStats() (complete, incomplete int)
	PacketsSeen() int
	LatePackets() int
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

func encodeHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}
