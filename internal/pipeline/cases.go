package pipeline

import (
	"github.com/pion/rtp"

	"medcheck/internal/adapter"
	"medcheck/internal/clock"
	medrtp "medcheck/internal/rtp"
)

// CaseResult is one program-generated edge-case fixture result.
type CaseResult struct {
	Name           string          `json:"scenario"`
	ErrorCode      clock.ErrorCode `json:"error_code"`
	ErrorCodes     map[string]int  `json:"error_codes"`
	Counts         map[string]int  `json:"counts"`
	FirstBadOffset int64           `json:"first_bad_offset,omitempty"`
	TargetChannels int             `json:"target_channels,omitempty"`
	TargetRate     int             `json:"target_sample_rate,omitempty"`
	ColorSource    string          `json:"color_source,omitempty"`
	OK             bool            `json:"ok"`
}

// RunCases generates and processes multichannel, conflict, color, and truncation fixtures.
func RunCases() []CaseResult {
	return []CaseResult{
		multichannelCase(),
		sampleRateConflictCase(),
		missingColorCase(),
		conflictColorCase(),
		truncatedPacketCase(),
	}
}

func multichannelCase() CaseResult {
	a := adapter.NewAACV1()
	in := adapter.AudioInput{
		Channels: 6, Layout: "5.1", SampleRate: 44100,
		Target:  adapter.AudioSpec{Channels: 6, Layout: "5.1", SampleRate: 48000},
		Samples: make([]int16, 6*480),
	}
	out, errs := a.Adapt(in)
	codes := countCodes(errs)
	return CaseResult{
		Name: "multichannel", ErrorCode: clock.CodeAudioChannelMapped, ErrorCodes: codes,
		Counts: map[string]int{
			"input_frames": out.InputFrames, "output_frames": out.OutputFrames,
			"target_channels": out.Spec.Channels, "target_sample_rate": out.Spec.SampleRate,
		},
		TargetChannels: out.Spec.Channels, TargetRate: out.Spec.SampleRate, OK: out.Spec.SampleRate == 48000,
	}
}

func sampleRateConflictCase() CaseResult {
	a := adapter.NewAACV1()
	out, errs := a.Adapt(adapter.AudioInput{
		Channels: 2, SampleRate: 44100, ContainerSampleRate: 22050,
		Target:  adapter.AudioSpec{Channels: 2, SampleRate: 48000},
		Samples: make([]int16, 2*100),
	})
	codes := countCodes(errs)
	return CaseResult{
		Name: "sample_rate_conflict", ErrorCode: clock.CodeSampleRateConflict, ErrorCodes: codes,
		Counts:     map[string]int{"conflicts": codes[string(clock.CodeSampleRateConflict)], "output_frames": out.OutputFrames},
		TargetRate: out.Spec.SampleRate, OK: codes[string(clock.CodeSampleRateConflict)] == 1,
	}
}

func missingColorCase() CaseResult {
	h := adapter.NewH264V1()
	out, errs := h.Adapt(adapter.VideoInput{Width: 640, Height: 360})
	codes := countCodes(errs)
	return CaseResult{
		Name: "color_metadata_missing", ErrorCode: clock.CodeColorMetadataDefaulted, ErrorCodes: codes,
		Counts:      map[string]int{"defaulted": codes[string(clock.CodeColorMetadataDefaulted)]},
		ColorSource: out.Spec.Color.Source, OK: out.Spec.Color.Source != "",
	}
}

func conflictColorCase() CaseResult {
	h := adapter.NewH264V1()
	out, errs := h.Adapt(adapter.VideoInput{Color: adapter.ColorMetadata{
		Present: true, Primaries: 1, Transfer: 6, Matrix: 6,
	}})
	codes := countCodes(errs)
	return CaseResult{
		Name: "color_metadata_conflict", ErrorCode: clock.CodeColorMetadataConflict, ErrorCodes: codes,
		Counts:      map[string]int{"conflicts": codes[string(clock.CodeColorMetadataConflict)]},
		ColorSource: out.Spec.Color.Source, OK: codes[string(clock.CodeColorMetadataConflict)] == 1,
	}
}

func truncatedPacketCase() CaseResult {
	valid, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 97, SSRC: 42, SequenceNumber: 9}, Payload: []byte("x")}).Marshal()
	if err != nil {
		panic(err)
	}
	in := medrtp.NewIngestor()
	offset := int64(7 * 256)
	_, errs := in.Ingest(valid[:8], offset)
	codes := map[string]int{}
	for _, e := range errs {
		codes[string(e.Code)]++
	}
	return CaseResult{
		Name: "truncated_packet", ErrorCode: clock.CodeInvalidPacket, ErrorCodes: codes,
		Counts:         map[string]int{"invalid": in.Stats.Invalid, "packets": in.Stats.Packets},
		FirstBadOffset: offset, OK: in.Stats.Invalid == 1 && errs[0].InputOffset == offset,
	}
}

func countCodes(errs []clock.MediaError) map[string]int {
	out := map[string]int{}
	for _, e := range errs {
		out[string(e.Code)]++
	}
	return out
}
