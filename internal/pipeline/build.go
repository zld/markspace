package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pion/rtp"

	"mediapack/internal/clock"
	"mediapack/internal/index"
	gomp4 "mediapack/internal/mp4"
	"mediapack/internal/stream"
	"mediapack/internal/subtitle"
)

func assetIDForLayer(layer string) string { return "mp4:" + layer }

func segmentRelPath(layer string, index int) string {
	parts := strings.Split(layer, "/")
	platform, rendition := parts[0], parts[1]
	return filepath.Join("mp4", platform, rendition, fmt.Sprintf("segment-%03d.mp4", index+1))
}

func segmentAbsPath(out, layer string, index int) string {
	return filepath.Join(out, segmentRelPath(layer, index))
}

func encodedPayload(p BuildParams, segment int) []byte {
	// A deterministic luma-like raster plus parameter byte. This stands in for
	// an encoded sample because the kernel intentionally does not decode arbitrary
	// consumer codecs.
	const n = 1024
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*7 + segment*37 + p.Width + p.BitrateKbps + int(p.Rendition[0])) & 0xff)
	}
	b[0] = byte(p.Width >> 8)
	b[1] = byte(p.Width)
	b[2] = byte(segment)
	return b
}

type builtSegment struct {
	segment stream.Segment
	data    []byte
	hash    string
}

func buildSegment(fx *Fixture, params BuildParams, layer string, idx int, injectCorruption bool) (builtSegment, error) {
	w := fx.Windows[idx]
	payload := encodedPayload(params, idx)
	decodeTime := uint64(w.Start)
	data, err := gomp4.BuildFragment(uint32(idx+1), decodeTime, payload)
	if err != nil {
		return builtSegment{}, err
	}

	if injectCorruption && layer == CorruptLayer && idx == CorruptSegmentZero {
		bad := uint64(w.Start + 3*fx.OutputRate.FrameDuration())
		if err := gomp4.CorruptDecodeTime(data, bad); err != nil {
			return builtSegment{}, err
		}
	}

	source := segmentRelPath(layer, idx)
	info, err := gomp4.AnalyzeFragment(source, data)
	if err != nil {
		return builtSegment{}, err
	}
	actualStart := clock.Ticks(info.DecodeTime)
	actualEnd := actualStart + (w.End - w.Start)
	hash := index.SHA256Hex(data)

	rtpStart := uint32(int64(w.Start) * int64(params.RTPClockRate) / clock.TicksPerSecond)
	rtpDuration := uint32(int64(w.End-w.Start) * int64(params.RTPClockRate) / clock.TicksPerSecond)
	packets := []rtp.Packet{
		{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(idx*2 + 1), Timestamp: rtpStart, SSRC: 0x5155414b}, Payload: payload[:min(128, len(payload))]},
		{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(idx*2 + 2), Timestamp: rtpStart + rtpDuration/2, SSRC: 0x5155414b}, Payload: payload[128:256]},
	}

	return builtSegment{
		data: data,
		hash: hash,
		segment: stream.Segment{
			Layer:      layer,
			Index:      idx,
			Start:      actualStart,
			End:        actualEnd,
			ByteOffset: int64(info.MoofOffset),
			SHA256:     hash,
			Packets:    packets,
		},
	}, nil
}

func writeSegment(out, layer string, idx int, data []byte) error {
	path := segmentAbsPath(out, layer, idx)
	return index.AtomicWriteFile(path, data, 0o644)
}

func verifyExistingSegmentHash(out, layer string, idx int, want string) error {
	path := segmentAbsPath(out, layer, idx)
	got, err := index.FileSHA256(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("segment %s index %d hash mismatch: checkpoint=%s disk=%s", layer, idx, want, got)
	}
	return nil
}

func writeSubtitleAssets(fx *Fixture, out string) (map[string]string, map[string]string, error) {
	paths := map[string]string{
		"srt": filepath.Join(out, "subtitles", "game-dialogue.srt"),
		"vtt": filepath.Join(out, "subtitles", "game-dialogue.vtt"),
	}
	if err := index.AtomicWriteFile(paths["srt"], fx.SRT, 0o644); err != nil {
		return nil, nil, err
	}
	if err := index.AtomicWriteFile(paths["vtt"], fx.VTT, 0o644); err != nil {
		return nil, nil, err
	}
	hashes := map[string]string{
		"srt": index.SHA256Hex(fx.SRT),
		"vtt": index.SHA256Hex(fx.VTT),
	}

	// Exercise half-open cross-segment splitting and merge verification.
	var cuts []clock.Ticks
	for _, w := range fx.Windows {
		cuts = append(cuts, w.Start, w.End)
	}
	for _, cue := range fx.Cues {
		parts := subtitle.SplitCueAt(cue, cuts)
		merged, err := subtitle.MergeFragments("pipeline", parts)
		if err != nil {
			return nil, nil, err
		}
		if merged.Start != cue.Start || merged.End != cue.End || merged.Text != cue.Text {
			return nil, nil, fmt.Errorf("cue %d split/merge mismatch", cue.Index)
		}
	}
	return paths, hashes, nil
}
