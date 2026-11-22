// Package fixture generates the local, seeded demonstration inputs: RTP
// datagrams carrying H.264, H.265 and AV1 access units, an external keyframe
// index, and the five fault classes the pipeline must survive. Every byte is
// derived from the seed, so identical arguments reproduce identical output.
//
// Fault plan for the default spec:
//   - duplicate: one copied datagram in each of the first two streams;
//   - reorder: four datagrams arrive a little later than their sequence;
//   - burst loss: consecutive H.264 datagrams vanish, shortening a keyframe
//     sample so its index offset becomes out of range and its segment hash
//     changes;
//   - retransmission: one H.265 burst is repaired before commit, so exactly
//     one gap is recovered;
//   - truncation: one AV1 datagram is cut mid-OBU, producing one damaged span
//     while later samples still scan cleanly;
//   - bad index: the shortened keyframe sample's index row is now out of
//     range, so 9 of 10 keyframe rows verify and the index is rebuilt.
package fixture

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediadist/internal/codec"
)

// Spec controls fixture generation.
type Spec struct {
	Seed             int64
	SegmentMS        int
	LossBurst        int
	BandwidthKbps    int
	PacketsPerSample int
	SampleCount      int
	KeyframeEvery    int
	// TruncatedSample selects the AV1 access unit to cut.
	TruncatedSample int
	// RepairedSample selects the H.265 access unit whose burst is repaired.
	RepairedSample int
}

// DefaultSpec mirrors the demo command line defaults.
func DefaultSpec() Spec {
	return Spec{
		Seed: 41, SegmentMS: 2000, LossBurst: 3, BandwidthKbps: 1800,
		PacketsPerSample: 4, SampleCount: 100, KeyframeEvery: 10,
		TruncatedSample: 7, RepairedSample: 40,
	}
}

func (s Spec) normalized() Spec {
	d := DefaultSpec()
	if s.SegmentMS <= 0 {
		s.SegmentMS = d.SegmentMS
	}
	if s.PacketsPerSample <= 0 {
		s.PacketsPerSample = d.PacketsPerSample
	}
	if s.SampleCount <= 0 {
		s.SampleCount = d.SampleCount
	}
	if s.KeyframeEvery <= 0 {
		s.KeyframeEvery = d.KeyframeEvery
	}
	if s.BandwidthKbps <= 0 {
		s.BandwidthKbps = d.BandwidthKbps
	}
	if s.TruncatedSample <= 0 {
		s.TruncatedSample = d.TruncatedSample
	}
	if s.RepairedSample <= 0 {
		s.RepairedSample = d.RepairedSample
	}
	return s
}

// Stream is one logical RTP stream carrying one codec.
type Stream struct {
	Name      string
	Codec     string // h264, h265, av1
	TrackID   uint32
	Timescale uint32
	// Origin is the first RTP timestamp, deliberately non-zero.
	Origin uint32
	// Datagrams is the arrival order handed to the transport stage.
	Datagrams [][]byte
	// Repairs are retransmitted datagrams keyed by sequence, delivered after
	// the reorder buffer declares the corresponding hole.
	Repairs map[uint16][]byte
	// AccessUnits is the clean media in decode order.
	AccessUnits []AccessUnit
	// seqOf[accessUnit][chunk] is the RTP sequence of each chunk.
	seqOf [][]uint16
}

// AccessUnit is one coded picture before packetization.
type AccessUnit struct {
	Index         int
	Keyframe      bool
	Data          []byte
	KeyUnitOffset int64
	Timestamp     uint32
}

// Fixture is the whole generated input set.
type Fixture struct {
	Spec      Spec
	Streams   []*Stream
	IndexJSON []byte
	Faults    FaultSummary
}

// FaultSummary counts injected faults by class.
type FaultSummary struct {
	Duplicates  int
	Reordered   int
	LossBursts  int
	LostPackets int
	Repairs     int
	Truncated   int
	BadIndex    int
}

// Generate builds every stream and the index for spec.
func Generate(spec Spec) (*Fixture, error) {
	spec = spec.normalized()
	if spec.SampleCount < 4 || spec.SampleCount/spec.KeyframeEvery < 2 {
		return nil, fmt.Errorf("fixture: need at least two keyframes, got %d samples every %d",
			spec.SampleCount, spec.KeyframeEvery)
	}
	if spec.LossBurst < 2 || spec.LossBurst >= spec.PacketsPerSample {
		return nil, fmt.Errorf("fixture: loss burst %d must be in 2..%d",
			spec.LossBurst, spec.PacketsPerSample-1)
	}
	if spec.TruncatedSample >= spec.SampleCount || spec.RepairedSample >= spec.SampleCount {
		return nil, fmt.Errorf("fixture: fault samples must be below %d", spec.SampleCount)
	}
	f := &Fixture{Spec: spec}
	streams := []*Stream{
		{Name: "h264-main", Codec: "h264", TrackID: 1, Timescale: 90000, Origin: 0xFFFFE000},
		{Name: "h265-main", Codec: "h265", TrackID: 2, Timescale: 90000, Origin: 30000},
		{Name: "av1-main", Codec: "av1", TrackID: 3, Timescale: 90000, Origin: 9000},
	}
	for _, st := range streams {
		buildAccessUnits(st, spec)
		packetize(st, spec)
	}
	// Loss first, so duplicate and reorder faults do not interact with it.
	injectBurstLoss(streams[0], spec, &f.Faults, false)
	injectBurstLoss(streams[1], spec, &f.Faults, true)
	injectTruncation(streams[2], spec, &f.Faults)
	injectDuplicates(streams, &f.Faults)
	injectReordering(streams, &f.Faults)
	f.IndexJSON, f.Faults.BadIndex = buildIndex(streams, spec)
	f.Streams = streams
	return f, nil
}

func h264AccessUnit(index int, key bool, salt int64) []byte {
	nals := [][]byte{
		{0x67, 0x4D, 0x40, 0x1F, byte(0xE9 + salt%7), 0x04}, // SPS
		{0x68, 0xEE, 0x3C, 0x80},                            // PPS
	}
	slice := make([]byte, 40+int(salt%17))
	for i := range slice {
		slice[i] = byte((index*31 + i*7 + int(salt)) & 0xFF)
	}
	if key {
		slice[0] = 0x65
	} else {
		slice[0] = 0x41
	}
	nals = append(nals, slice)
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

func h265AccessUnit(index int, key bool, salt int64) []byte {
	vps := []byte{0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00}
	slice := make([]byte, 36+int(salt%13))
	for i := range slice {
		slice[i] = byte((index*17 + i*5 + int(salt)) & 0xFF)
	}
	if key {
		slice[0], slice[1] = 0x26, 0x01
	} else {
		slice[0], slice[1] = 0x02, 0x01
	}
	var out []byte
	for _, n := range [][]byte{vps, sps, slice} {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

// av1AccessUnit builds a temporal delimiter, a sequence header, an optional
// frame header OBU and a frame OBU. Splitting header and frame keeps the
// uncompressed frame header in a predictable place: its first byte is
// show_existing_frame(1)|frame_type(2), so KEY_FRAME (0) is 0x00 and
// INTER_FRAME (1) is 0x20.
func av1AccessUnit(index int, key bool, salt int64) []byte {
	var out []byte
	out = append(out, 0x12, 0x00) // temporal delimiter: type 2, has_size, zero payload
	seq := []byte{0x00, 0x00, 0x00, byte(0x20 + index%4)}
	out = append(out, 0x0A, byte(len(seq))) // sequence header, type 1, has size
	out = append(out, seq...)
	fh := []byte{0x00, 0x01, 0x02, 0x03} // frame header OBU payload
	out = append(out, 0x1A, byte(len(fh))) // frame header, type 3, has size
	out = append(out, fh...)
	payload := make([]byte, 20+int(salt%11))
	for i := range payload {
		payload[i] = byte((index*13 + i*3 + int(salt)) & 0xFF)
	}
	ft := uint8(0)
	if !key {
		ft = 1 // INTER_FRAME
	}
	payload[0] = ft << 5
	payload[1] = 0x00
	out = append(out, 0x32, byte(len(payload))) // frame OBU, type 6, has size
	out = append(out, payload...)
	return out
}

func buildAccessUnits(st *Stream, spec Spec) {
	const frameTicks = 3000 // 30 fps at 90 kHz
	st.AccessUnits = make([]AccessUnit, 0, spec.SampleCount)
	for i := 0; i < spec.SampleCount; i++ {
		key := i%spec.KeyframeEvery == 0
		salt := int64(i)*7 + spec.Seed
		var data []byte
		switch st.Codec {
		case "h264":
			data = h264AccessUnit(i, key, salt)
		case "h265":
			data = h265AccessUnit(i, key, salt)
		default:
			data = av1AccessUnit(i, key, salt)
		}
		u := AccessUnit{
			Index: i, Keyframe: key, Data: data, KeyUnitOffset: -1,
			Timestamp: st.Origin + uint32(i*frameTicks),
		}
		if key {
			if off, ok := keyUnitOffset(st.Codec, data); ok {
				u.KeyUnitOffset = off
			}
		}
		st.AccessUnits = append(st.AccessUnits, u)
	}
}

// keyUnitOffset locates the keyframe-bearing NAL/OBU with the same scanners
// the pipeline uses, so index rows and scan results agree by construction.
// Only the frame OBU counts for AV1, not the sequence header.
func keyUnitOffset(c string, data []byte) (int64, bool) {
	var res codec.ScanResult
	switch c {
	case "h264":
		res = codec.ScanH264(0, data, codec.DefaultScanLimits())
	case "h265":
		res = codec.ScanH265(0, data, codec.DefaultScanLimits())
	case "av1":
		res = codec.ScanAV1(0, data, codec.DefaultScanLimits())
		for _, ui := range res.Keyframes {
			if res.Units[ui].Type == codec.OBUFrame {
				return res.Units[ui].Offset, true
			}
		}
		return -1, false
	}
	for _, ui := range res.Keyframes {
		return res.Units[ui].Offset, true
	}
	return -1, false
}

// packetize splits each access unit into PacketsPerSample RTP packets, marks
// the last, and records the sequence numbers of every chunk.
func packetize(st *Stream, spec Spec) {
	n := spec.PacketsPerSample
	st.Repairs = make(map[uint16][]byte)
	st.seqOf = make([][]uint16, len(st.AccessUnits))
	seq := uint16(1000)
	for ai, u := range st.AccessUnits {
		chunk := (len(u.Data) + n - 1) / n
		if chunk == 0 {
			chunk = 1
		}
		for off := 0; off < len(u.Data); off += chunk {
			end := off + chunk
			if end > len(u.Data) {
				end = len(u.Data)
			}
			p := &rtp.Packet{
				Header: rtp.Header{
					Version: 2, PayloadType: payloadType(st.Codec),
					SequenceNumber: seq, Timestamp: u.Timestamp,
					SSRC: uint32(0x1000 + st.TrackID), Marker: end == len(u.Data),
				},
				Payload: u.Data[off:end],
			}
			raw, err := p.Marshal()
			if err != nil {
				panic(err)
			}
			st.Datagrams = append(st.Datagrams, raw)
			st.seqOf[ai] = append(st.seqOf[ai], seq)
			seq++
		}
	}
}

func payloadType(c string) uint8 {
	switch c {
	case "h264":
		return 96
	case "h265":
		return 97
	default:
		return 98
	}
}

// injectBurstLoss removes LossBurst consecutive datagrams inside one access
// unit. With repair=true the removed datagrams become retransmissions sized to
// arrive while the hole is still pending.
func injectBurstLoss(st *Stream, spec Spec, fs *FaultSummary, repair bool) {
	target := 0
	if repair {
		target = spec.RepairedSample
	}
	if target >= len(st.seqOf) || len(st.seqOf[target]) < 2 {
		return
	}
	chunks := st.seqOf[target]
	start := 1
	if start+spec.LossBurst > len(chunks) {
		start = len(chunks) - spec.LossBurst
	}
	if start < 1 {
		start = 1
	}
	drop := make(map[uint16]struct{}, spec.LossBurst)
	for i := start; i < start+spec.LossBurst && i < len(chunks)-1; i++ {
		drop[chunks[i]] = struct{}{}
	}
	if len(drop) == 0 {
		return
	}
	kept := st.Datagrams[:0]
	removed := 0
	for _, dg := range st.Datagrams {
		seq := binary.BigEndian.Uint16(dg[2:4])
		if _, hit := drop[seq]; hit {
			removed++
			if repair {
				st.Repairs[seq] = append([]byte(nil), dg...)
				fs.Repairs++
			}
			continue
		}
		kept = append(kept, dg)
	}
	st.Datagrams = kept
	fs.LossBursts++
	fs.LostPackets += removed
}

// injectTruncation cuts the last datagram of the selected AV1 access unit so
// its frame OBU declares more bytes than remain. The RTP header and marker are
// kept, so the sample exists and exactly one OBU is damaged.
func injectTruncation(st *Stream, spec Spec, fs *FaultSummary) {
	target := spec.TruncatedSample
	if target >= len(st.seqOf) || len(st.seqOf[target]) == 0 {
		return
	}
	seq := st.seqOf[target][len(st.seqOf[target])-1]
	for i, dg := range st.Datagrams {
		if binary.BigEndian.Uint16(dg[2:4]) != seq {
			continue
		}
		if len(dg) <= 18 {
			return
		}
		cut := 12 + (len(dg)-12)/2
		st.Datagrams[i] = append([]byte(nil), dg[:cut]...)
		fs.Truncated++
		return
	}
}

func injectDuplicates(streams []*Stream, fs *FaultSummary) {
	for i, st := range streams {
		if i >= 2 || len(st.Datagrams) < 12 {
			continue
		}
		at := 6 + i
		st.Datagrams = insertAt(st.Datagrams, at+5, st.Datagrams[at])
		fs.Duplicates++
	}
}

func injectReordering(streams []*Stream, fs *FaultSummary) {
	moved := 0
	for _, st := range streams {
		for pos := 3; pos < len(st.Datagrams)-3 && moved < 4; pos += 11 {
			dg := st.Datagrams[pos]
			st.Datagrams = append(st.Datagrams[:pos], st.Datagrams[pos+1:]...)
			st.Datagrams = insertAt(st.Datagrams, pos+2, dg)
			moved++
		}
	}
	fs.Reordered = moved
}

func insertAt(list [][]byte, at int, v []byte) [][]byte {
	if at > len(list) {
		at = len(list)
	}
	list = append(list, nil)
	copy(list[at+1:], list[at:])
	list[at] = v
	return list
}

type indexRow struct {
	Stream   int   `json:"stream"`
	Sample   int   `json:"sample"`
	Offset   int64 `json:"offset"`
	Size     int64 `json:"size"`
	Keyframe bool  `json:"keyframe"`
	Sync     bool  `json:"sync,omitempty"`
}

// buildIndex writes an external index covering only the primary stream's
// keyframes. The loss fault removed bytes from one keyframe sample, so that
// row's offset is out of range and 9 of 10 rows verify.
func buildIndex(streams []*Stream, spec Spec) ([]byte, int) {
	var rows []indexRow
	st := streams[0]
	for _, u := range st.AccessUnits {
		if !u.Keyframe || u.KeyUnitOffset < 0 {
			continue
		}
		rows = append(rows, indexRow{
			Stream: 0, Sample: u.Index, Offset: u.KeyUnitOffset,
			Size: 4, Keyframe: true, Sync: true,
		})
	}
	b := make([]byte, 0, len(rows)*64+32)
	b = append(b, []byte(`{"version":1,"entries":[`)...)
	for i, r := range rows {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, []byte(fmt.Sprintf(
			`{"stream":%d,"sample":%d,"offset":%d,"size":%d,"keyframe":%t,"sync":%t}`,
			r.Stream, r.Sample, r.Offset, r.Size, r.Keyframe, r.Sync))...)
	}
	b = append(b, []byte("]}")...)
	return b, 1
}

// CanonicalFor returns the media-bearing bytes of a clean sample.
func CanonicalFor(c string, data []byte) []byte {
	switch c {
	case "h264", "h265":
		var res codec.ScanResult
		if c == "h264" {
			res = codec.ScanH264(0, data, codec.DefaultScanLimits())
		} else {
			res = codec.ScanH265(0, data, codec.DefaultScanLimits())
		}
		return codec.Canonical(res.Units, data)
	case "av1":
		res := codec.ScanAV1(0, data, codec.DefaultScanLimits())
		return codec.OBUCanonical(res.Units, data)
	default:
		return data
	}
}
