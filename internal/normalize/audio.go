package normalize

import (
	"encoding/binary"
	"fmt"
	"math"

	"mediarc/internal/model"
)

// Layout is an explicitly named channel order.
type Layout struct {
	Name     string
	Channels []string
}

// Known layouts are the only layouts this kernel will interpret. An unknown
// layout must produce exit code 3 rather than a guessed mono/stereo mix.
var (
	LayoutMono   = Layout{Name: "mono", Channels: []string{"FC"}}
	LayoutStereo = Layout{Name: "stereo", Channels: []string{"FL", "FR"}}
	Layout51     = Layout{Name: "5.1", Channels: []string{"FL", "FR", "FC", "LFE", "BL", "BR"}}
)

// LookupLayout returns a declared layout by name.
func LookupLayout(name string) (Layout, bool) {
	switch name {
	case "mono":
		return LayoutMono, true
	case "stereo", "2.0":
		return LayoutStereo, true
	case "5.1", "5.1(back)":
		return Layout51, true
	default:
		return Layout{}, false
	}
}

// MustLayout is used by tests and trusted defaults; user input uses Lookup.
func MustLayout(name string) Layout {
	l, ok := LookupLayout(name)
	if !ok {
		panic("unknown declared layout: " + name)
	}
	return l
}

// Route sends one source channel to one destination with Q15 gain.
type Route struct {
	From     string `json:"from"`
	To       string `json:"to"`
	GainQ15  int32  `json:"gain_q15"`
}

// UnityQ15 is gain 1.0 in fixed point.
const UnityQ15 int32 = 32768

// IdentityRoutes maps every channel to the same label at unity gain.
func IdentityRoutes(l Layout) []Route {
	r := make([]Route, 0, len(l.Channels))
	for _, c := range l.Channels {
		r = append(r, Route{From: c, To: c, GainQ15: UnityQ15})
	}
	return r
}

// DecodeS16BE decodes L16/big-endian signed PCM.
func DecodeS16BE(payload []byte) []int16 {
	out := make([]int16, len(payload)/2)
	for i := range out {
		out[i] = int16(binary.BigEndian.Uint16(payload[2*i : 2*i+2]))
	}
	return out
}

// EncodeS16BE encodes signed PCM as deterministic L16 bytes.
func EncodeS16BE(samples []int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.BigEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

// MapChannels applies an explicit route list to interleaved PCM.
func MapChannels(samples []int16, from Layout, to Layout, routes []Route) ([]int16, error) {
	if len(samples)%len(from.Channels) != 0 {
		return nil, fmt.Errorf("pcm length %d not divisible by source channels %d", len(samples), len(from.Channels))
	}
	sourceIndex := make(map[string]int, len(from.Channels))
	for i, c := range from.Channels {
		sourceIndex[c] = i
	}
	targetIndex := make(map[string]int, len(to.Channels))
	for i, c := range to.Channels {
		targetIndex[c] = i
	}
	type wired struct{ src, dst int; gain int32 }
	var ws []wired
	for _, r := range routes {
		si, ok1 := sourceIndex[r.From]
		di, ok2 := targetIndex[r.To]
		if !ok1 || !ok2 {
			return nil, model.NewExitError(model.ExitSyncConfig, "invalid channel route",
				model.Fault{Kind: model.FaultUnknownChannel, Expected: r.From + "->" + r.To,
					Message: "route references an undeclared channel"})
		}
		ws = append(ws, wired{si, di, r.GainQ15})
	}
	frames := len(samples) / len(from.Channels)
	out := make([]int16, frames*len(to.Channels))
	for f := 0; f < frames; f++ {
		acc := make([]int32, len(to.Channels))
		for _, w := range ws {
			acc[w.dst] += int32(samples[f*len(from.Channels)+w.src]) * w.gain / UnityQ15
		}
		for c, v := range acc {
			out[f*len(to.Channels)+c] = clip16(v)
		}
	}
	return out, nil
}

func clip16(v int32) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

// Resample interleaved PCM by deterministic linear interpolation.
func Resample(samples []int16, channels int, inRate, outRate uint32) []int16 {
	if channels <= 0 {
		channels = 1
	}
	if inRate == 0 || outRate == 0 || inRate == outRate {
		return append([]int16(nil), samples...)
	}
	inFrames := len(samples) / channels
	outFrames := (uint64(inFrames)*uint64(outRate) + uint64(inRate)/2) / uint64(inRate)
	out := make([]int16, int(outFrames)*channels)
	for of := uint64(0); of < outFrames; of++ {
		num := of * uint64(inRate)
		idx := num / uint64(outRate)
		fracNum := num % uint64(outRate)
		for c := 0; c < channels; c++ {
			a := int32(samples[int(idx)*channels+c])
			b := a
			if idx+1 < uint64(inFrames) {
				b = int32(samples[int(idx+1)*channels+c])
			}
			v := a + (b-a)*int32(fracNum)/int32(outRate)
			out[int(of)*channels+c] = clip16(v)
		}
	}
	return out
}

// AudioResult records conversion facts for the acceptance summary.
type AudioResult struct {
	Samples          []int16 `json:"-"`
	InputRate        uint32  `json:"input_rate"`
	OutputRate       uint32  `json:"output_rate"`
	InputChannels    int     `json:"input_channels"`
	OutputChannels   int     `json:"output_channels"`
	Resampled        bool    `json:"resampled"`
	MappedChannels   bool    `json:"mapped_channels"`
}

// AudioSpec is the complete deterministic normalization request.
type AudioSpec struct {
	InputRate  uint32
	OutputRate uint32
	Input      Layout
	Output     Layout
	Routes     []Route
}

// NormalizeAudio decodes L16BE, applies explicit channel routing, then
// resamples. Unknown input/output layouts are rejected before any guessing.
func NormalizeAudio(payload []byte, spec AudioSpec) (AudioResult, error) {
	if spec.Input.Name == "" || spec.Output.Name == "" {
		return AudioResult{}, model.NewExitError(model.ExitSyncConfig, "channel layout required",
			model.Fault{Kind: model.FaultUnknownChannel, Message: "empty channel layout"})
	}
	if len(spec.Routes) == 0 {
		spec.Routes = IdentityRoutes(spec.Input)
		if spec.Input.Name != spec.Output.Name {
			return AudioResult{}, model.NewExitError(model.ExitSyncConfig, "explicit channel map required",
				model.Fault{Kind: model.FaultUnknownChannel, Actual: spec.Input.Name + "->" + spec.Output.Name})
		}
	}
	decoded := DecodeS16BE(payload)
	mapped, err := MapChannels(decoded, spec.Input, spec.Output, spec.Routes)
	if err != nil {
		return AudioResult{}, err
	}
	resampled := Resample(mapped, len(spec.Output.Channels), spec.InputRate, spec.OutputRate)
	return AudioResult{
		Samples: resampled, InputRate: spec.InputRate, OutputRate: spec.OutputRate,
		InputChannels: len(spec.Input.Channels), OutputChannels: len(spec.Output.Channels),
		Resampled: spec.InputRate != spec.OutputRate, MappedChannels: spec.Input.Name != spec.Output.Name,
	}, nil
}
