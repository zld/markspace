package render

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"math"
	"sort"
)

// Renderer renders timelines.
type Renderer struct {
	cfg Config
}

// New validates cfg.
func New(cfg Config) (*Renderer, error) {
	validated, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Renderer{cfg: validated}, nil
}

// Config returns the effective configuration.
func (r *Renderer) Config() Config { return r.cfg }

// RenderFrames renders one frame per timeline slot. Slots are sorted by
// (StartNS, Seq) first, so unsorted input still yields a stable result.
func (r *Renderer) RenderFrames(t Timeline) []Frame {
	slots := sortedSlots(t.Slots)
	frames := make([]Frame, 0, len(slots))
	for i, slot := range slots {
		frames = append(frames, r.renderFrame(i, slot))
	}
	return frames
}

// RenderAudio renders one audio chunk per timeline slot.
func (r *Renderer) RenderAudio(t Timeline) []AudioChunk {
	slots := sortedSlots(t.Slots)
	chunks := make([]AudioChunk, 0, len(slots))
	for i, slot := range slots {
		chunks = append(chunks, r.renderAudioChunk(i, slot))
	}
	return chunks
}

func sortedSlots(slots []Slot) []Slot {
	sorted := make([]Slot, len(slots))
	copy(sorted, slots)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartNS != sorted[j].StartNS {
			return sorted[i].StartNS < sorted[j].StartNS
		}
		return sorted[i].Seq < sorted[j].Seq
	})
	return sorted
}

// baseColour derives a stable colour from the slot.
func baseColour(slot Slot) RGB {
	// FNV-1a over the identifying fields; no dependency on map iteration or
	// clock, so the colour is stable across runs.
	var buf [17]byte
	binary.LittleEndian.PutUint64(buf[0:], uint64(slot.StartNS))
	binary.LittleEndian.PutUint64(buf[8:], slot.Seq)
	buf[16] = byte(slot.Kind)
	h := fnv.New32a()
	_, _ = h.Write(buf[:])
	sum := h.Sum32()
	switch slot.Kind {
	case SlotSilence:
		// Silence is a calm blue-grey, independent of the source hash, so
		// recovered regions are obvious in the statistics.
		return RGB{R: 24, G: 32, B: 56}
	case SlotGap:
		return RGB{R: 200, G: 24, B: 24}
	default:
		return RGB{
			R: uint8(40 + sum%180),
			G: uint8(40 + (sum>>8)%180),
			B: uint8(40 + (sum>>16)%180),
		}
	}
}

func (r *Renderer) renderFrame(index int, slot Slot) Frame {
	canvas := r.cfg.Canvas
	pixels := make([]RGB, canvas.Pixels())
	colour := baseColour(slot)
	for i := range pixels {
		pixels[i] = colour
	}
	// A marker bar encodes the frame index, wrapping around the canvas width.
	markerX := index % canvas.Width
	markerY := (index / canvas.Width) % canvas.Height
	marker := RGB{R: 255 - colour.R, G: 255 - colour.G, B: 255 - colour.B}
	pixels[markerY*canvas.Width+markerX] = marker
	// Recovered slots get a second marker in the opposite corner so frame and
	// audio recovery are both visible in the pixel statistics.
	if slot.Kind != SlotMedia {
		pixels[(canvas.Height-1)*canvas.Width+(canvas.Width-1)] = marker
	}
	frame := Frame{
		Index:      index,
		StartNS:    slot.StartNS,
		DurationNS: slot.DurationNS,
		Recovered:  slot.Kind != SlotMedia,
		Canvas:     canvas,
		Pixels:     pixels,
	}
	h := fnv.New64a()
	writePixels(h, pixels)
	frame.Hash = h.Sum64()
	return frame
}

func writePixels(h hash.Hash64, pixels []RGB) {
	var buf [3]byte
	for _, pixel := range pixels {
		buf[0], buf[1], buf[2] = pixel.R, pixel.G, pixel.B
		_, _ = h.Write(buf[:])
	}
}

// renderAudioChunk synthesises a deterministic waveform for media and silence
// for recovered regions. Media is a sine at a frequency derived from the slot
// position, so loudness varies measurably but reproducibly.
func (r *Renderer) renderAudioChunk(index int, slot Slot) AudioChunk {
	sampleRate := r.cfg.SampleRate
	channels := r.cfg.Channels
	frames := slot.DurationNS * int64(sampleRate) / 1_000_000_000
	if frames < 0 {
		frames = 0
	}
	data := make([]byte, frames*int64(channels)*2)
	if slot.Kind == SlotMedia {
		frequency := 220.0 + float64(int(slot.Seq)%8)*55.0
		for i := int64(0); i < frames; i++ {
			seconds := float64(i) / float64(sampleRate)
			phase := 2 * math.Pi * frequency * seconds
			sample := int16(math.Round(0.5 * math.Sin(phase) * 32767))
			for ch := 0; ch < channels; ch++ {
				offset := (i*int64(channels) + int64(ch)) * 2
				binary.LittleEndian.PutUint16(data[offset:], uint16(sample))
			}
		}
	}
	chunk := AudioChunk{
		Index:      index,
		StartNS:    slot.StartNS,
		DurationNS: slot.DurationNS,
		Recovered:  slot.Kind != SlotMedia,
		SampleRate: sampleRate,
		Channels:   channels,
		Data:       data,
	}
	h := fnv.New64a()
	_, _ = h.Write(data)
	chunk.Hash = h.Sum64()
	return chunk
}
