package main

import (
	"fmt"
	"strconv"
	"strings"

	"assetcheck/internal/stream"
)

// thresholdFlag accepts repeated -threshold flags whose value is a comma
// separated k=v list. Unspecified keys keep the acceptance defaults.
type thresholdFlag struct {
	t stream.Thresholds
}

func defaultThresholdFlag() thresholdFlag {
	return thresholdFlag{t: stream.DefaultThresholds()}
}

func (f *thresholdFlag) String() string {
	return fmt.Sprintf("sample-rate=%d,channels=%d,min-duration-seconds=%g,max-gap-frames=%d,max-corrupt-ratio=%g",
		f.t.SampleRate, f.t.Channels, f.t.MinDurationSeconds, f.t.MaxGapFrames, f.t.MaxCorruptRatio)
}

func (f *thresholdFlag) Set(v string) error {
	for _, pair := range strings.Split(v, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("threshold %q must be key=value", pair)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if err := f.apply(key, val); err != nil {
			return err
		}
	}
	return nil
}

func (f *thresholdFlag) apply(key, val string) error {
	switch key {
	case "sample-rate", "samplerate":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid sample-rate %q", val)
		}
		f.t.SampleRate = n
	case "channels", "ch":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid channels %q", val)
		}
		f.t.Channels = n
	case "min-duration-seconds", "duration-seconds", "duration", "min-duration":
		n, err := strconv.ParseFloat(val, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid duration %q", val)
		}
		f.t.MinDurationSeconds = n
	case "max-gap-frames", "max-gap":
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid max-gap-frames %q", val)
		}
		f.t.MaxGapFrames = n
	case "max-corrupt-ratio", "corrupt-ratio":
		n, err := strconv.ParseFloat(val, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid max-corrupt-ratio %q", val)
		}
		f.t.MaxCorruptRatio = n
	default:
		return fmt.Errorf("unknown threshold key %q", key)
	}
	return nil
}
