package timebase

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"

	"mediarc/model"
)

// Timecode is a wall-clock frame label: HH:MM:SS:FF.
type Timecode struct {
	Hours   int64
	Minutes int64
	Seconds int64
	Frames  int64
}

// String renders the timecode with ":" separators.
func (tc Timecode) String() string {
	return fmt.Sprintf("%02d:%02d:%02d:%02d", tc.Hours, tc.Minutes, tc.Seconds, tc.Frames)
}

// ParseTimecode parses "HH:MM:SS:FF" (also accepting ';' before the frame
// count, the conventional drop-frame marker).
func ParseTimecode(text string, rate Rate) (Timecode, error) {
	trimmed := strings.TrimSpace(text)
	sep := byte(':')
	if strings.ContainsRune(trimmed, ';') {
		if !rate.DropFrame {
			return Timecode{}, model.New(model.CodeIllegalDropFrame, stream,
				fmt.Sprintf("drop-frame separator in %q but rate %s is not drop-frame", text, rate))
		}
		sep = ';'
	}
	parts := strings.Split(trimmed, string(sep))
	if len(parts) != 4 {
		return Timecode{}, model.New(model.CodeInvalidTimecode, stream,
			fmt.Sprintf("timecode %q must be HH:MM:SS%sFF", text, string(sep)))
	}
	fields := make([]int64, 4)
	for i, part := range parts {
		if part == "" || len(part) > 2 {
			return Timecode{}, model.New(model.CodeInvalidTimecode, stream,
				fmt.Sprintf("timecode %q field %d is not one or two digits", text, i))
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil || v < 0 {
			return Timecode{}, model.New(model.CodeInvalidTimecode, stream,
				fmt.Sprintf("timecode %q field %q is not a number", text, part))
		}
		fields[i] = v
	}
	tc := Timecode{Hours: fields[0], Minutes: fields[1], Seconds: fields[2], Frames: fields[3]}
	if tc.Minutes >= 60 || tc.Seconds >= 60 {
		return Timecode{}, model.New(model.CodeInvalidTimecode, stream,
			fmt.Sprintf("timecode %q has out-of-range minutes or seconds", text))
	}
	if tc.Frames >= rate.NominalFPS() {
		return Timecode{}, model.New(model.CodeInvalidTimecode, stream,
			fmt.Sprintf("timecode %q frame %d exceeds nominal rate %d", text, tc.Frames, rate.NominalFPS()))
	}
	return tc, nil
}

// ToNanos converts the label into nanoseconds on the shared timeline.
func (r Rate) ToNanos(tc Timecode) (int64, error) {
	if tc.Hours < 0 || tc.Minutes < 0 || tc.Seconds < 0 || tc.Frames < 0 {
		return 0, model.New(model.CodeNegativeTime, stream, "timecode components must not be negative")
	}
	nominal := r.NominalFPS()
	if tc.Minutes >= 60 || tc.Seconds >= 60 || tc.Frames >= nominal {
		return 0, model.New(model.CodeInvalidTimecode, stream,
			fmt.Sprintf("timecode %s out of range for %s", tc, r))
	}
	hoursToMinutes, err := checkedMul(tc.Hours, 60)
	if err != nil {
		return 0, err
	}
	totalMinutes, err := checkedAdd(hoursToMinutes, tc.Minutes)
	if err != nil {
		return 0, err
	}
	var frame int64
	if r.DropFrame {
		if totalMinutes%10 != 0 && tc.Seconds == 0 && tc.Frames < 2 {
			return 0, model.New(model.CodeIllegalDropFrame, stream,
				fmt.Sprintf("timecode %s names a dropped frame", tc))
		}
		base, err := checkedMul(totalMinutes, nominal*60)
		if err != nil {
			return 0, err
		}
		secFrames, err := checkedMul(tc.Seconds, nominal)
		if err != nil {
			return 0, err
		}
		rest, err := checkedAdd(secFrames, tc.Frames)
		if err != nil {
			return 0, err
		}
		base, err = checkedAdd(base, rest)
		if err != nil {
			return 0, err
		}
		dropped := 2 * (totalMinutes - totalMinutes/10)
		frame, err = checkedSub(base, dropped)
		if err != nil {
			return 0, err
		}
	} else {
		minutesInSeconds, err := checkedMul(totalMinutes, 60)
		if err != nil {
			return 0, err
		}
		seconds, err := checkedAdd(minutesInSeconds, tc.Seconds)
		if err != nil {
			return 0, err
		}
		framesPerMinute, err := checkedMul(seconds, nominal)
		if err != nil {
			return 0, err
		}
		frame, err = checkedAdd(framesPerMinute, tc.Frames)
		if err != nil {
			return 0, err
		}
	}
	return r.frameToNanos(frame)
}

// NanosToFrames floors ns onto the frame grid of r.
func (r Rate) NanosToFrames(ns int64) (int64, error) {
	if ns < 0 {
		return 0, model.New(model.CodeNegativeTime, stream, "timestamp must not be negative")
	}
	if r.Den <= 0 || r.Num <= 0 {
		return 0, model.New(model.CodeInvalidTimecode, stream, "frame rate must be positive")
	}
	if r.Den > math.MaxInt64/billion {
		return 0, model.New(model.CodeTimestampOverflow, stream, "frame rate denominator is too large")
	}
	frameNanos := r.Den * billion // exact duration of one frame in nanoseconds
	hi, lo := bits.Mul64(uint64(ns), uint64(r.Num))
	if hi >= uint64(frameNanos) {
		return 0, model.New(model.CodeTimestampOverflow, stream, "frame index overflows int64")
	}
	q, _ := bits.Div64(hi, lo, uint64(frameNanos))
	if q > math.MaxInt64 {
		return 0, model.New(model.CodeTimestampOverflow, stream, "frame index overflows int64")
	}
	return int64(q), nil
}

func (r Rate) frameToNanos(frame int64) (int64, error) {
	if frame < 0 {
		return 0, model.New(model.CodeNegativeTime, stream, "frame index must not be negative")
	}
	if r.Num <= 0 || r.Den <= 0 || r.Den > math.MaxInt64/billion {
		return 0, model.New(model.CodeInvalidTimecode, stream, "frame rate must be positive and representable")
	}
	scale := r.Den * billion
	if frame != 0 && scale > math.MaxInt64/frame {
		return 0, model.New(model.CodeTimestampOverflow, stream,
			fmt.Sprintf("frame %d overflows the nanosecond timeline", frame))
	}
	num := frame * scale
	q, rem := num/r.Num, num%r.Num
	if rem != 0 && rem >= (r.Num+1)/2 {
		if q == math.MaxInt64 {
			return 0, model.New(model.CodeTimestampOverflow, stream, "rounded timestamp overflows int64")
		}
		q++
	}
	return q, nil
}

// FrameNanos is the exact, rounded duration of one frame of r.
func (r Rate) FrameNanos() (int64, error) {
	return r.frameToNanos(1)
}
