package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/pion/rtp"

	"assetpack/internal/clock"
)

const RTPMagic = "RTP1"

// RTPDescriptor is the small versioned SDP-equivalent for local RTP fixtures.
type RTPDescriptor struct {
	APIVersion int            `json:"api_version"`
	Tracks     []RTPTrackDesc `json:"tracks"`
}

type RTPTrackDesc struct {
	PayloadType   uint8  `json:"payload_type"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Codec         ID     `json:"codec"`
	ClockRate     int64  `json:"clock_rate"`
	FrameDuration uint32 `json:"frame_duration"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	Channels      int    `json:"channels,omitempty"`
}

type rtpSeqState struct {
	trackID string
	have    bool
	last    uint16
}

type RTPV1 struct{}

func NewRTPV1() *RTPV1 { return &RTPV1{} }
func (a *RTPV1) Descriptor() Descriptor {
	return Descriptor{ID: ID{Name: FormatRTP, Version: APIVersion}, Kind: "transport"}
}
func (a *RTPV1) Detect(head []byte) bool { return len(head) >= 4 && string(head[:4]) == RTPMagic }

func (a *RTPV1) Parse(ctx context.Context, src Source) (*Asset, error) {
	ra, size, err := src.Reader()
	if err != nil {
		return nil, err
	}
	if c, ok := ra.(io.Closer); ok {
		defer c.Close()
	}
	if size > MaxParseWindow {
		return nil, fmt.Errorf("%w: RTP input %d exceeds parse window %d", ErrBadInput, size, MaxParseWindow)
	}
	data, err := io.ReadAll(io.NewSectionReader(ra, 0, size))
	if err != nil {
		return nil, err
	}
	sum := sha256Hex(data)
	if len(data) < 8 || string(data[:4]) != RTPMagic {
		return nil, fmt.Errorf("%w: missing %q magic", ErrBadInput, RTPMagic)
	}
	descLen := binary.BigEndian.Uint32(data[4:8])
	if uint64(8+descLen) > uint64(len(data)) || descLen > uint32(MaxParseWindow) {
		return nil, fmt.Errorf("%w: invalid RTP descriptor length %d", ErrBadInput, descLen)
	}
	var desc RTPDescriptor
	if err := json.Unmarshal(data[8:8+descLen], &desc); err != nil {
		return nil, clock.Wrap("rtp", "decode-descriptor", 8, err)
	}
	if desc.APIVersion != APIVersion {
		return nil, fmt.Errorf("%w: rtp/v%d", ErrUnknownVersion, desc.APIVersion)
	}
	byPT := map[uint8]RTPTrackDesc{}
	tracks := map[string]*Track{}
	order := []string{}
	seq := map[uint32]*rtpSeqState{}
	off := int(8 + descLen)

	for off < len(data) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frameOff := off
		if len(data)-off < 2 {
			return nil, locatedRTP("rtp", "frame-header", int64(frameOff), "truncated length")
		}
		n := int(binary.BigEndian.Uint16(data[off : off+2]))
		off += 2
		if n <= 12 || off+n > len(data) {
			return nil, locatedRTP("rtp", "frame-bounds", int64(frameOff),
				fmt.Sprintf("packet length %d exceeds input", n))
		}
		raw := data[off : off+n]
		off += n
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, locatedRTP("rtp", "unmarshal", int64(frameOff), err.Error())
		}
		td, ok := byPT[pkt.PayloadType]
		if !ok {
			for _, cand := range desc.Tracks {
				if cand.PayloadType == pkt.PayloadType {
					td = cand
					ok = true
					break
				}
			}
			if !ok {
				return nil, locatedRTP(fmt.Sprintf("pt-%d", pkt.PayloadType), "codec", int64(frameOff),
					fmt.Sprintf("%w: payload type %d", ErrUnknownCodec, pkt.PayloadType))
			}
			byPT[pkt.PayloadType] = td
		}
		st, ok := seq[pkt.SSRC]
		if !ok {
			st = &rtpSeqState{trackID: td.ID}
			seq[pkt.SSRC] = st
		}
		if st.have {
			delta := int((pkt.SequenceNumber - st.last) & 0xffff)
			switch {
			case delta == 0:
				return nil, locatedRTP(td.ID, "rtp-seq", int64(frameOff),
					fmt.Sprintf("duplicate sequence %d", pkt.SequenceNumber))
			case delta != 1:
				return nil, locatedRTP(td.ID, "rtp-seq", int64(frameOff),
					fmt.Sprintf("missing %d packet(s) after sequence %d, got %d", delta-1, st.last, pkt.SequenceNumber))
			}
		}
		st.have, st.last = true, pkt.SequenceNumber

		tb := clock.Timescale(td.ClockRate)
		pm, err := clock.Map(td.ID, "rtp-ts", int64(frameOff), int64(pkt.Timestamp), tb)
		if err != nil {
			return nil, err
		}
		headerSize := pkt.Header.MarshalSize()
		payloadOff := int64(frameOff + 2 + headerSize)
		tr, exists := tracks[td.ID]
		if !exists {
			tr = &Track{
				ID: td.ID, Kind: td.Kind, Codec: td.Codec, Timebase: tb,
				Width: td.Width, Height: td.Height, SampleRate: td.SampleRate, Channels: td.Channels,
			}
			tracks[td.ID] = tr
			order = append(order, td.ID)
		}
		tr.Samples = append(tr.Samples, Sample{
			Index: len(tr.Samples), TrackID: td.ID, PTS: pm.Ticks,
			Duration: 0, Offset: payloadOff, Size: len(pkt.Payload),
			Key: pkt.Marker, Seq: pkt.SequenceNumber, Payload: append([]byte(nil), pkt.Payload...),
		})
	}

	asset := &Asset{Descriptor: a.Descriptor(), SourceName: src.Name, SourceSHA: sum}
	for _, id := range order {
		tr := tracks[id]
		var fallback clock.Ticks
		for i := range tr.Samples {
			if i+1 < len(tr.Samples) {
				tr.Samples[i].Duration = tr.Samples[i+1].PTS - tr.Samples[i].PTS
				fallback = tr.Samples[i].Duration
			} else if fallback > 0 {
				tr.Samples[i].Duration = fallback
			} else {
				for _, td := range desc.Tracks {
					if td.ID == id {
						dm, _ := clock.Map(id, "rtp-duration", 0, int64(td.FrameDuration), tr.Timebase)
						tr.Samples[i].Duration = dm.Ticks
					}
				}
			}
		}
		tr.Duration = 0
		if len(tr.Samples) > 0 {
			last := tr.Samples[len(tr.Samples)-1]
			tr.Duration = last.PTS + last.Duration
		}
		asset.Tracks = append(asset.Tracks, tr)
		if tr.Duration > asset.Duration {
			asset.Duration = tr.Duration
		}
	}
	return asset, nil
}

func locatedRTP(track, stage string, offset int64, msg string) error {
	return clock.Wrap(track, stage, offset, fmt.Errorf("%w: %s", ErrBadInput, msg))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
