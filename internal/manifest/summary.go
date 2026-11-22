package manifest

import (
	"crypto/sha256"
	"encoding/hex"

	"mediacore/internal/audio"
)

// alignmentError compares each packet's manifest millisecond position with
// the position implied by the cumulative sample count, both measured from the
// first packet. Only relative timing is meaningful: the absolute RTP base is
// arbitrary and may sit near the 32-bit wraparound point.
func alignmentError(packets []Packet, rate int) float64 {
	if len(packets) == 0 || rate <= 0 {
		return 0
	}
	baseMs := packets[0].StartMs
	var worst float64
	var samples int64
	for _, p := range packets {
		sampleMs := 1000 * float64(samples) / float64(rate)
		diff := float64(p.StartMs-baseMs) - sampleMs
		if diff < 0 {
			diff = -diff
		}
		if diff > worst {
			worst = diff
		}
		samples += int64(p.Samples)
	}
	return worst
}

// digest covers the run's results. The file path is excluded because it is
// not part of the produced distribution; two runs over identical media bytes
// therefore share a digest even if the file lives at a different path.
func digest(m *Manifest) string {
	type alias Manifest
	c := *m
	c.Summary = ""
	c.File = ""
	cp := alias(c)
	// json.Marshal cannot error for this shape.
	data, _ := marshalStable(cp)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sliceFrames returns a view copy of frames [start,end) retaining the layout
// and start offset of the source.
func sliceFrames(src *audio.Buffer, start, end int) *audio.Buffer {
	out, err := audio.NewBuffer(src.Layout, end-start)
	if err != nil {
		return &audio.Buffer{Layout: src.Layout}
	}
	ch := src.Layout.NumChannels()
	copy(out.Data, src.Data[start*ch:end*ch])
	out.Start = src.Start + int64(start)
	out.Metadata = src.Metadata
	return out
}
