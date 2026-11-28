package fixture

import (
	"github.com/pion/rtp"

	"medcheck/internal/mp4"
)

// WrapSeries is a modular tick sequence that crosses a modulus boundary.
type WrapSeries struct {
	Name     string
	Modulus  uint64
	Raw      []uint64
	Expected []int64
}

// WrapAround returns a 16-bit-style series wrapping once.
func WrapAround() WrapSeries {
	return WrapSeries{
		Name:     "wrap",
		Modulus:  1 << 16,
		Raw:      []uint64{65530, 65533, 65535, 2, 5},
		Expected: []int64{65530, 65533, 65535, 65538, 65541},
	}
}

// RTPDisorder is an out-of-order packet sequence plus its in-order target.
type RTPDisorder struct {
	Name     string
	SSRC     uint32
	Window   uint16
	Arrival  []uint16 // sequence numbers in arrival order
	Expected []uint16 // recovered order
}

// DisorderedRTP builds a packet series shuffled within the reorder window.
func DisorderedRTP() RTPDisorder {
	return RTPDisorder{
		Name:     "rtp",
		SSRC:     4242,
		Window:   8,
		Arrival:  []uint16{0, 2, 1, 5, 3, 4, 6},
		Expected: []uint16{0, 1, 2, 3, 4, 5, 6},
	}
}

// Packets marshals the arrival order into real RTP packets.
func (d RTPDisorder) Packets() []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(d.Arrival))
	for _, seq := range d.Arrival {
		out = append(out, &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: seq, SSRC: d.SSRC, PayloadType: 96, Marker: true},
			Payload: []byte{byte(seq)},
		})
	}
	return out
}

// MP4Sample is a generated MP4 stream and its expected key frame sample indexes.
type MP4Sample struct {
	Name        string
	Data        []byte
	TrackID     uint32
	Timescale   uint32
	KeySamples  []uint32
	SampleCount int
}

// ValidMP4 builds a clean single-track MP4 with periodic key frames.
func ValidMP4() (*MP4Sample, error) {
	b := mp4.NewBuilder(1, 90000)
	for i := 0; i < 9; i++ {
		b.AddSample(mp4.SampleSpec{
			Payload: []byte{byte(i), byte(i + 1), byte(i + 2), byte(i + 3)},
			Delta:   3000,
			IsKey:   i%3 == 0,
		})
	}
	data, err := b.Build()
	if err != nil {
		return nil, err
	}
	return &MP4Sample{
		Name: "mp4", Data: data, TrackID: 1, Timescale: 90000,
		KeySamples: []uint32{0, 3, 6}, SampleCount: 9,
	}, nil
}

// TruncatedMP4 returns a valid stream with an over-long trailing box appended.
func TruncatedMP4() (*MP4Sample, error) {
	base, err := ValidMP4()
	if err != nil {
		return nil, err
	}
	data := append([]byte{}, base.Data...)
	data = append(data, 0, 0, 0, 40, 'm', 'd', 'a', 't')
	data = append(data, 1, 2, 3)
	return &MP4Sample{
		Name: "mp4_trunc", Data: data, TrackID: 1, Timescale: 90000,
		KeySamples: base.KeySamples, SampleCount: base.SampleCount,
	}, nil
}
