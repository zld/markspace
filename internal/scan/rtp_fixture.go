package scan

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediacore/internal/fault"
)

// RTPPacketSpec is one generated packet. Slice order is arrival order.
type RTPPacketSpec struct {
	SSRC      uint32
	PayloadTS uint8
	Sequence  uint16
	Timestamp uint32
	Marker    bool
	Data      []byte
}

// RTPFixtureSpec describes a generated length-prefixed RTP fixture.
type RTPFixtureSpec struct {
	Streams []RTPStreamMeta
	Packets []RTPPacketSpec
}

type rtpFileHeader struct {
	Streams []RTPStreamMeta `json:"streams"`
}

// WriteRTPFixture writes the local deterministic RTP fixture format.
func WriteRTPFixture(path string, spec RTPFixtureSpec) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	meta, err := json.Marshal(rtpFileHeader{Streams: spec.Streams})
	if err != nil {
		return fault.Wrap(fault.InvalidArgument, err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	defer f.Close()

	if _, err := f.WriteString(RTPMagic); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, 0)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(meta)))
	if _, err := f.Write(lenBuf[:]); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, int64(len(RTPMagic)))
	}
	if _, err := f.Write(meta); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, int64(len(RTPMagic)+4))
	}
	for _, ps := range spec.Packets {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         ps.Marker,
				PayloadType:    ps.PayloadTS,
				SequenceNumber: ps.Sequence,
				Timestamp:      ps.Timestamp,
				SSRC:           ps.SSRC,
			},
			Payload: ps.Data,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return fault.Wrap(fault.InvalidArgument, err)
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		if _, err := f.Write(lenBuf[:]); err != nil {
			return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
		}
		if _, err := f.Write(raw); err != nil {
			return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
		}
	}
	return nil
}

// ReadRTPFixturePackets reads and parses all packets in arrival order.
func ReadRTPFixturePackets(path string) ([]*rtp.Packet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.NotFound, err), path, -1)
	}
	defer f.Close()
	if _, err := readRTPHeader(f, path); err != nil {
		return nil, err
	}
	var out []*rtp.Packet
	var lenBuf [4]byte
	for {
		_, err := io.ReadFull(f, lenBuf[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fault.At(fault.Wrap(fault.TruncatedData, err), path, mustCurrent(f))
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == 0 || n > 1<<20 {
			return nil, fault.At(fault.New(fault.CorruptHeader, "invalid RTP record length"), path, mustCurrent(f)-4)
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(f, raw); err != nil {
			return nil, fault.At(fault.Wrap(fault.TruncatedData, err), path, mustCurrent(f))
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), path, mustCurrent(f)-int64(n))
		}
		out = append(out, &pkt)
	}
	return out, nil
}

func buildRTPIndex(idx *Index) error {
	path := idx.SourcePath
	f, err := os.Open(path)
	if err != nil {
		return fault.At(fault.Wrap(fault.NotFound, err), path, -1)
	}
	defer f.Close()
	streams, err := readRTPHeader(f, path)
	if err != nil {
		return err
	}
	idx.RTPStreams = streams
	bySSRC := map[uint32]RTPStreamMeta{}
	for _, s := range streams {
		bySSRC[s.SSRC] = s
		idx.Tracks = append(idx.Tracks, Track{
			ID:         s.TrackID,
			Handler:    map[string]string{"video": "vide", "audio": "soun"}[s.Kind],
			Kind:       s.Kind,
			Codec:      s.Codec,
			BoxOffset:  0,
			Timescale:  s.Timescale,
			Channels:   s.Channels,
			SampleRate: s.SampleRate,
			DurationNS: 0,
		})
	}

	var lenBuf [4]byte
	arrival := 0
	for {
		recordOff := mustCurrent(f)
		_, err := io.ReadFull(f, lenBuf[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return fault.At(fault.Wrap(fault.TruncatedData, err), path, recordOff)
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == 0 || n > 1<<20 {
			return fault.At(fault.New(fault.CorruptHeader, "invalid RTP packet length"), path, recordOff)
		}
		packetOff := recordOff + 4
		raw := make([]byte, n)
		if _, err := io.ReadFull(f, raw); err != nil {
			return fault.At(fault.Wrap(fault.TruncatedData, err), path, packetOff)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return fault.At(fault.Wrap(fault.CorruptHeader, err), path, packetOff)
		}
		meta, ok := bySSRC[pkt.SSRC]
		if !ok {
			return fault.At(fault.New(fault.UnsupportedTrack, "RTP SSRC has no stream mapping"), path, packetOff)
		}
		payloadOff := packetOff + int64(pkt.Header.PayloadOffset)
		if pkt.Header.PayloadOffset <= 0 || payloadOff+int64(len(pkt.Payload)) > idx.SourceSize {
			// Pion usually fills PayloadOffset; derive from raw payload suffix
			// if a future version leaves the deprecated field zero.
			payloadOff = packetOff + int64(len(raw)-len(pkt.Payload))
		}
		idx.Packets = append(idx.Packets, PacketRef{
			ArrivalIndex: arrival,
			RecordOffset: recordOff,
			PacketOffset: packetOff,
			PayloadOff:   payloadOff,
			Length:       int(n),
			SSRC:         pkt.SSRC,
			PayloadType:  pkt.PayloadType,
			Sequence:     pkt.SequenceNumber,
			Timestamp:    pkt.Timestamp,
			Marker:       pkt.Marker,
			TrackID:      meta.TrackID,
		})
		arrival++
	}
	if len(idx.Packets) == 0 {
		return fault.At(fault.New(fault.TruncatedData, "RTP fixture contains no packets"), path, 0)
	}
	return nil
}

func readRTPHeader(f *os.File, path string) ([]RTPStreamMeta, error) {
	magic := make([]byte, len(RTPMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), path, 0)
	}
	if string(magic) != RTPMagic {
		return nil, fault.At(fault.New(fault.UnsupportedFormat, "bad RTP fixture magic"), path, 0)
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
		return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), path, int64(len(RTPMagic)))
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 1<<20 {
		return nil, fault.At(fault.New(fault.CorruptHeader, "invalid RTP metadata length"), path, int64(len(RTPMagic)))
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(f, raw); err != nil {
		return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), path, int64(len(RTPMagic)+4))
	}
	var h rtpFileHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), path, int64(len(RTPMagic)+4))
	}
	if len(h.Streams) == 0 {
		return nil, fault.At(fault.New(fault.UnsupportedTrack, "RTP metadata has no streams"), path, int64(len(RTPMagic)+4))
	}
	return h.Streams, nil
}

func mustCurrent(f *os.File) int64 {
	off, _ := f.Seek(0, io.SeekCurrent)
	return off
}
