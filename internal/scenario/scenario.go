// Package scenario generates fully self-contained replay fixtures in
// memory. Nothing is read from external files: RTP, RTCP and MP4 assets
// are synthesised deterministically from the case name so that every run
// of a case produces byte-identical inputs.
package scenario

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"replay/internal/fault"
	"replay/internal/graph"
	"replay/internal/media"
)

// Case identifiers accepted on the command line.
const (
	CaseNormal       = "normal"
	CaseSequenceWrap = "seq-wrap"
	// CaseConflict is the acceptance name for the duplicate/content
	// conflict scenario.
	CaseConflict = "conflict"
	// CaseDuplicateConflict is the descriptive alias for CaseConflict.
	CaseDuplicateConflict = CaseConflict
	CaseTruncatedRTCP     = "truncated-rtcp"
	CaseDependencyCycle   = "dependency-cycle"
	// CaseResume is the normal pipeline under a case name dedicated to
	// stop/resume acceptance, so its workspace is isolated from normal.
	CaseResume = "resume"
)

// RTPPayload is one datagram in input order together with the byte
// offset it occupies in the logical capture.
type RTPPayload struct {
	Track  string
	Bytes  []byte
	Offset int64
}

// RTCPPayload is one RTCP datagram in input order.
type RTCPPayload struct {
	Track  string
	Bytes  []byte
	Offset int64
	// Truncate, when positive, cuts this many bytes off Bytes to
	// simulate a truncated datagram while keeping the declared length.
	Truncate int
}

// AssetSpec describes one MP4 asset to generate.
type AssetSpec struct {
	Version       string
	Track         string
	DurationNS    int64
	TrackOffsetNS int64
	ContentKey    string
	Salt          byte
}

// Fixture is everything a replay run needs.
type Fixture struct {
	Case     string
	Versions []string
	Tracks   []graph.Track
	RTP      []RTPPayload
	RTCP     []RTCPPayload
	Assets   []AssetSpec
	// WantInterval is the range the mapped assets must cover.
	WantInterval [2]int64
	// ExpectError indicates the case is expected to fail, with its kind.
	ExpectError fault.Kind
}

// RTPSSRC returns the stable SSRC for a track.
func RTPSSRC(track string) uint32 {
	var sum uint32 = 2166136261
	for i := 0; i < len(track); i++ {
		sum ^= uint32(track[i])
		sum *= 16777619
	}
	return sum
}

// RTPClockRate is the fixture clock rate for a track.
func RTPClockRate(track string) uint32 {
	if track == "video" {
		return 90000
	}
	return 48000
}

// sequencesFor builds a packet plan for a case. It returns the datagrams
// in transmission (input) order, deliberately shuffled and duplicated.
func sequencesFor(cs string, track string, count int, startSeq uint16, wrapSeq bool) []packetPlan {
	var plans []packetPlan
	for i := 0; i < count; i++ {
		seq := startSeq + uint16(i)
		if wrapSeq {
			// Start near the 16-bit ceiling so the run wraps once.
			seq = startSeq + uint16(i)
		}
		plans = append(plans, packetPlan{
			Seq:  seq,
			TS:   uint32(i) * (RTPClockRate(track) / 50), // 20 ms slots
			data: payloadFor(track, i, cs),
		})
	}
	return plans
}

type packetPlan struct {
	Seq  uint16
	TS   uint32
	data []byte
}

func payloadFor(track string, index int, cs string) []byte {
	// Payload bytes model encoded media frames; content is deterministic
	// and version-free so identical packets hash identically.
	out := media.EssenceFor(track, fmt.Sprintf("frame-%d", index), 0, 40)
	copy(out, []byte(track))
	_ = cs
	return out
}

// Build creates the fixture for a case. Versions must be non-empty; the
// CLI passes "v1,v2" style values. Cycles are produced only for the
// dependency-cycle case.
func Build(cs string, versions []string) (Fixture, error) {
	if len(versions) == 0 {
		return Fixture{}, fault.New(fault.KindArgument, "scenario requires at least one version").At("scenario")
	}
	fx := Fixture{Case: cs, Versions: append([]string(nil), versions...)}

	switch cs {
	case CaseNormal, CaseSequenceWrap, CaseConflict, CaseTruncatedRTCP, CaseResume:
		fx.Tracks = []graph.Track{
			{ID: "audio-main", ClockRate: 48000, Version: versions[0]},
			{ID: "video-main", ClockRate: 90000, Version: versions[0], DependsOn: []string{"audio-main"}},
		}
		fx.WantInterval = [2]int64{0, 1_000_000_000}
	case CaseDependencyCycle:
		fx.Tracks = []graph.Track{
			{ID: "audio-main", ClockRate: 48000, DependsOn: []string{"video-main"}},
			{ID: "video-main", ClockRate: 90000, DependsOn: []string{"audio-main"}},
		}
		fx.ExpectError = fault.KindGraph
		fx.WantInterval = [2]int64{0, 1_000_000_000}
	default:
		return Fixture{}, fault.New(fault.KindArgument, "unknown scenario case "+cs).At("scenario")
	}

	// --- RTP for audio-main and video-main ---
	// Audio runs on 48 kHz and video on 90 kHz, so the same 20 ms frame
	// cadence yields different tick deltas per track.
	var offset int64
	for _, track := range []string{"audio-main", "video-main"} {
		count := 25
		startSeq := uint16(1000)
		wrap := cs == CaseSequenceWrap && track == "audio-main"
		if wrap {
			startSeq = 65520
			count = 35 // crosses the 16-bit ceiling while staying under 2^32 ts
		}
		plans := sequencesFor(cs, track, count, startSeq, wrap)
		plans = reorderPlans(plans)
		// Drop one mid-run packet so the ingest reports a real deficit
		// while all later packets stay inside the reorder window.
		dropped := false
		for _, p := range plans {
			if !dropped && p.Seq == startSeq+10 {
				dropped = true
				continue
			}
			raw := marshalRTP(track, p.Seq, p.TS, p.data)
			fx.RTP = append(fx.RTP, RTPPayload{Track: track, Bytes: raw, Offset: offset})
			offset += int64(len(raw))
		}
	}

	if cs == CaseConflict {
		// A duplicate of an already accepted packet with different
		// content: same sequence, same timestamp, different bytes.
		raw := marshalRTP("audio-main", 1000, 0, []byte("conflicting-duplicate-payload"))
		fx.RTP = append(fx.RTP, RTPPayload{Track: "audio-main", Bytes: raw, Offset: offset})
		offset += int64(len(raw))
	}

	// --- RTCP sender reports ---
	rtcpOffset := int64(0)
	ntpBase := uint64(0x83aa7e80) << 32 // Unix epoch in NTP seconds
	for _, track := range []string{"audio-main", "video-main"} {
		raw := marshalSR(RTPSSRC(track), ntpBase, 0, 25)
		payload := RTCPPayload{Track: track, Bytes: raw, Offset: rtcpOffset}
		if cs == CaseTruncatedRTCP && track == "audio-main" {
			payload.Truncate = 6
		}
		fx.RTCP = append(fx.RTCP, payload)
		rtcpOffset += int64(len(raw))
	}

	// --- MP4 assets ---
	// Both versions carry the same essence for a track, so overlaps merge
	// instead of conflicting. The last version is shifted by 250 ms to
	// exercise overlap handling, and the union still covers the whole
	// requested window. The duplicate-conflict case gives v1's audio a
	// different essence, which leaves a real content conflict.
	for _, version := range versions {
		for _, track := range []string{"audio-main", "video-main"} {
			contentKey := track + "/shared"
			salt := byte(0)
			if cs == CaseConflict && version == versions[0] && track == "audio-main" {
				contentKey = track + "/conflict"
				salt = 7
			}
			spec := AssetSpec{
				Version:    version,
				Track:      track,
				DurationNS: 1_000_000_000,
				ContentKey: contentKey,
				Salt:       salt,
			}
			if len(versions) > 1 && version == versions[len(versions)-1] {
				spec.TrackOffsetNS = 250_000_000
			}
			fx.Assets = append(fx.Assets, spec)
		}
	}
	return fx, nil
}

// reorderPlans produces a deterministic, slightly out-of-order
// transmission order with a duplicate of one packet.
func reorderPlans(plans []packetPlan) []packetPlan {
	if len(plans) < 4 {
		return plans
	}
	out := make([]packetPlan, 0, len(plans)+1)
	// Swap a couple of adjacent pairs.
	swaps := [][2]int{{1, 2}, {5, 6}, {9, 10}}
	index := make(map[int]int, len(swaps))
	for _, s := range swaps {
		index[s[0]] = s[1]
		index[s[1]] = s[0]
	}
	for i, p := range plans {
		if j, ok := index[i]; ok {
			out = append(out, plans[j])
			delete(index, i)
			continue
		}
		out = append(out, p)
	}
	// Duplicate packet 3 (identical payload) once.
	out = append(out[:4], append([]packetPlan{plans[3]}, out[4:]...)...)
	return out
}

func marshalRTP(track string, seq uint16, ts uint32, payload []byte) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           RTPSSRC(track),
			PayloadType:    111,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic("scenario: rtp marshal failed: " + err.Error())
	}
	return raw
}

// marshalSR builds an RTCP sender report by hand so the declared length
// always matches the real one; Marshal would do the same, but this keeps
// the fixture independent of library version details.
func marshalSR(ssrc uint32, ntp uint64, rtpTime, packetCount uint32) []byte {
	const size = 28
	buf := make([]byte, size)
	buf[0] = 0x80 // V=2, no padding, zero report blocks
	buf[1] = 200  // SR
	binary.BigEndian.PutUint16(buf[2:4], uint16(size/4-1))
	binary.BigEndian.PutUint32(buf[4:8], ssrc)
	binary.BigEndian.PutUint64(buf[8:16], ntp)
	binary.BigEndian.PutUint32(buf[16:20], rtpTime)
	binary.BigEndian.PutUint32(buf[20:24], packetCount)
	// Octet count left zero; it does not affect the timeline.
	return buf
}

// AssetBytes builds the MP4 bytes for one asset spec.
func AssetBytes(spec AssetSpec) ([]byte, error) {
	if spec.DurationNS <= 0 {
		return nil, fmt.Errorf("scenario: asset duration must be positive")
	}
	return media.Build(media.Asset{
		TrackID:       1,
		Timescale:     1000,
		DurationTicks: uint32(spec.DurationNS / 1_000_000),
		Essence:       media.EssenceFor(spec.Track, spec.ContentKey, spec.Salt, 96),
	})
}

// SortedTracks returns track IDs in canonical order.
func (f Fixture) SortedTracks() []string {
	ids := make([]string, 0, len(f.Tracks))
	for _, t := range f.Tracks {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	return ids
}

// KnownCases lists every accepted case name for help output.
func KnownCases() []string {
	return []string{
		CaseNormal,
		CaseSequenceWrap,
		CaseDuplicateConflict,
		CaseTruncatedRTCP,
		CaseDependencyCycle,
		CaseResume,
	}
}

// RTPCount returns the number of RTP datagrams the fixture supplies.
func (f Fixture) RTPCount() int { return len(f.RTP) }

// RTCPCount returns the number of RTCP datagrams the fixture supplies.
func (f Fixture) RTCPCount() int { return len(f.RTCP) }
