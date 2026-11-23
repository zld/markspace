// Package clock provides a single rational time base: frame rates, DF/NDF
// timecode conversion and a common tick timeline shared by all modules.
package clock

import (
	"fmt"
	"strings"

	"mediqc/internal/fault"
)

// Timescale is the common tick rate of the unified timeline (ticks/second).
// 90000 divides evenly by every supported frame duration.
const Timescale = 90000

// Rate is a rational frame rate in frames per second.
type Rate struct {
	Num uint32 // frames
	Den uint32 // seconds
}

var (
	FPS24   = Rate{24, 1}
	FPS25   = Rate{25, 1}
	FPS2997 = Rate{30000, 1001} // 29.97, DF-capable
	FPS50   = Rate{50, 1}
)

// Supported returns the set of rates this build converts.
func Supported() []Rate { return []Rate{FPS24, FPS25, FPS2997, FPS50} }

func (r Rate) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%d", r.Num)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// Nominal is the integer frame count used for timecode display
// (30 for 29.97).
func (r Rate) Nominal() int {
	return int((uint64(r.Num) + uint64(r.Den) - 1) / uint64(r.Den))
}

// DropCapable reports whether the rate supports drop-frame timecode.
func (r Rate) DropCapable() bool { return r == FPS2997 }

// TicksPerFrame is the exact duration of one frame in timeline ticks.
func (r Rate) TicksPerFrame() int64 {
	return Timescale * int64(r.Den) / int64(r.Num)
}

// FramesToTicks converts a frame count to timeline ticks (exact).
func FramesToTicks(frames int64, r Rate) int64 {
	return frames * r.TicksPerFrame()
}

// TicksToFrames converts timeline ticks to the nearest frame count.
func TicksToFrames(ticks int64, r Rate) int64 {
	tpf := r.TicksPerFrame()
	if ticks >= 0 {
		return (ticks + tpf/2) / tpf
	}
	return -((-ticks + tpf/2) / tpf)
}

// Timecode is a wall-clock frame address at a given rate.
type Timecode struct {
	HH, MM, SS, FF int
	Drop           bool
	Rate           Rate
}

func (tc Timecode) String() string {
	sep := ":"
	if tc.Drop {
		sep = ";"
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", tc.HH, tc.MM, tc.SS, sep, tc.FF)
}

// dropPerMinute is the number of frame labels dropped per minute in DF mode.
const dropPerMinute = 2

// FramesToTimecode converts a frame count to a timecode.
func FramesToTimecode(frames int64, r Rate, drop bool) (Timecode, error) {
	if frames < 0 {
		return Timecode{}, fault.New(fault.CodeBadTimecode, 0, -1,
			"negative frame count", "clamp to zero before conversion")
	}
	if drop && !r.DropCapable() {
		return Timecode{}, fault.New(fault.CodeUnsupportedRate, 0, -1,
			"drop-frame only defined for 29.97", "use NDF for rate "+r.String())
	}
	nom := int64(r.Nominal())
	f := frames
	if drop {
		// Real frame numbers skip dropped labels; map back to label space.
		perMin := nom*60 - dropPerMinute
		per10Min := perMin*10 + dropPerMinute // minute 0 of 10 drops nothing
		d := f / per10Min
		m := f % per10Min
		extra := int64(0)
		if m >= dropPerMinute {
			extra = (m - dropPerMinute) / perMin
		}
		f = frames + dropPerMinute*(9*d+extra)
	}
	total := f / nom
	if total >= 24*3600 {
		return Timecode{}, fault.New(fault.CodeDayOverflow, 0, -1,
			fmt.Sprintf("frame %d crosses the 24h day boundary", frames),
			"split assets at midnight or use a continuous frame index")
	}
	ff := int(f % nom)
	ss := int(total % 60)
	mm := int((total / 60) % 60)
	hh := int(total / 3600)
	return Timecode{HH: hh, MM: mm, SS: ss, FF: ff, Drop: drop, Rate: r}, nil
}

// TimecodeToFrames converts a timecode to a frame count, rejecting
// out-of-range fields, day overflow and dropped-frame labels in DF mode.
func TimecodeToFrames(tc Timecode) (int64, error) {
	nom := tc.Rate.Nominal()
	if tc.HH < 0 || tc.HH >= 24 || tc.MM < 0 || tc.MM >= 60 ||
		tc.SS < 0 || tc.SS >= 60 || tc.FF < 0 || tc.FF >= nom {
		return 0, fault.New(fault.CodeBadTimecode, 0, -1,
			fmt.Sprintf("timecode %s out of range (rate %s)", tc.String(), tc.Rate),
			"fields must satisfy HH<24 MM<60 SS<60 FF<nominal")
	}
	if tc.Drop {
		if !tc.Rate.DropCapable() {
			return 0, fault.New(fault.CodeUnsupportedRate, 0, -1,
				"drop-frame only defined for 29.97", "use NDF for rate "+tc.Rate.String())
		}
		if tc.SS == 0 && tc.MM%10 != 0 && tc.FF < dropPerMinute {
			return 0, fault.New(fault.CodeBadDropFrame, 0, -1,
				fmt.Sprintf("dropped frame label %s used", tc.String()),
				"DF timecode skips frames 00 and 01 at minute boundaries")
		}
		totalMin := int64(tc.HH)*60 + int64(tc.MM)
		return (totalMin*60+int64(tc.SS))*int64(nom) + int64(tc.FF) -
			dropPerMinute*(totalMin-totalMin/10), nil
	}
	return (int64(tc.HH)*3600+int64(tc.MM)*60+int64(tc.SS))*int64(nom) + int64(tc.FF), nil
}

// ParseTimecode parses "HH:MM:SS:FF" (NDF) or "HH:MM:SS;FF" (DF).
func ParseTimecode(s string, r Rate) (Timecode, error) {
	drop := strings.Contains(s, ";")
	sep := ":"
	if drop {
		sep = ";"
	}
	var hh, mm, ss, ff int
	n, err := fmt.Sscanf(s, "%02d:%02d:%02d"+sep+"%02d", &hh, &mm, &ss, &ff)
	if err != nil || n != 4 {
		return Timecode{}, fault.New(fault.CodeBadTimecode, 0, -1,
			"malformed timecode "+s, "want HH:MM:SS:FF or HH:MM:SS;FF")
	}
	tc := Timecode{HH: hh, MM: mm, SS: ss, FF: ff, Drop: drop, Rate: r}
	if _, err := TimecodeToFrames(tc); err != nil {
		return Timecode{}, err
	}
	return tc, nil
}
