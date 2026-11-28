package fixture

import (
	"encoding/binary"
	"fmt"
)

// Demo is a fully generated acceptance scenario. Every sample is produced from
// a seed, so the same seed always yields identical bytes and therefore identical
// fingerprints and manifest digests.
type Demo struct {
	// Files are the inputs to process, in rendition order.
	Files []DemoFile
	// Damaged holds inputs that must be rejected, with the reason.
	Damaged []DamagedFile
	// Descriptions explains each generated anomaly for the terminal report.
	Descriptions []string
}

// DemoFile is one generated input.
type DemoFile struct {
	Name  string
	Bytes []byte
	IsRTP bool
	Role  string // "video", "rtp", ...
}

// DamagedFile is an input that must be rejected, with the failure it encodes.
type DamagedFile struct {
	Name   string
	Bytes  []byte
	Reason string
}

// DemoOptions tunes the generated scenario.
type DemoOptions struct {
	Seed int64
	// Loss is the fraction of RTP packets to drop (0..1).
	Loss float64
	// Frames per rendition at 30 fps.
	Frames int
	// GOP is the keyframe interval in frames.
	GOP int
}

// BuildDemo generates the acceptance scenario: two aligned video renditions with
// subtitle cues, an RTP stream with duplicates, reorders, timestamp wrap and
// loss, a truncated emsg input and a same-path replacement input.
func BuildDemo(opts DemoOptions) Demo {
	if opts.Frames <= 0 {
		opts.Frames = 6000 // 200 s at 30 fps: enough segments for >2 chunks at --chunk-size 64
	}
	if opts.GOP <= 0 {
		opts.GOP = 30
	}
	rng := newDemoRand(opts.Seed)
	var d Demo

	cues := demoCues()
	for i, spec := range []struct {
		name  string
		id    uint32
		width uint16
	}{{"prog_360p.mp4", 1, 640}, {"prog_720p.mp4", 2, 1280}} {
		bytes, descs := buildDemoClip(spec.id, spec.width, opts, cues, rng)
		d.Files = append(d.Files, DemoFile{Name: spec.name, Bytes: bytes, Role: "video"})
		if i == 0 {
			d.Descriptions = append(d.Descriptions, descs...)
		}
	}

	// RTP stream mirroring the video timing, with duplicates, reorders, a
	// timestamp wrap and configured loss.
	rtpBytes, rtpDesc := buildDemoRTP(opts, rng)
	d.Files = append(d.Files, DemoFile{Name: "prog_stream.rtp", Bytes: rtpBytes, IsRTP: true, Role: "rtp"})
	d.Descriptions = append(d.Descriptions, rtpDesc...)

	// Damaged inputs.
	d.Damaged = append(d.Damaged, DamagedFile{
		Name:   "truncated_emsg.mp4",
		Bytes:  buildTruncatedEmsgClip(opts, rng),
		Reason: "emsg declares a string and message longer than the box",
	})
	d.Damaged = append(d.Damaged, DamagedFile{
		Name:   "truncated_chpl.mp4",
		Bytes:  buildTruncatedChplClip(opts, rng),
		Reason: "chpl chapter header runs past the box",
	})
	return d
}

// demoRand is a tiny deterministic PRNG (splitmix64) so the demo does not depend
// on math/rand's output stability across Go releases.
type demoRand struct{ state uint64 }

func newDemoRand(seed int64) *demoRand {
	return &demoRand{state: uint64(seed)*0x9e3779b97f4a7c15 + 0x1234567}
}

func (r *demoRand) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *demoRand) float() float64 { return float64(r.next()>>11) / float64(1<<53) }

func (r *demoRand) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// buildDemoClip writes one video rendition with a subtitle and audio track and a
// udta/chpl chapter list. It returns the bytes and the anomalies it encoded.
func buildDemoClip(id uint32, width uint16, opts DemoOptions, cues []string, rng *demoRand) ([]byte, []string) {
	var descs []string
	var video []Sample
	for i := 0; i < opts.Frames; i++ {
		payload := make([]byte, 32+rng.intn(16))
		for j := range payload {
			payload[j] = byte(rng.next())
		}
		video = append(video, Sample{Data: payload, DurationTicks: 3000, Sync: i%opts.GOP == 0})
	}
	// Audio at 48 kHz, 1024-tick frames (~21.3 ms), roughly 1.4x video length.
	var audio []Sample
	for i := 0; i < opts.Frames*2; i++ {
		audio = append(audio, Sample{Data: bytes3(byte(id), byte(i)), DurationTicks: 1024, Sync: true})
	}
	// Subtitle cues: one every cueIntervalTicks with a small deterministic
	// presentation jitter. The jitter is what av_sync_ms measures against the
	// nearest video frame; keeping it under 20 ms holds the run inside the 40 ms
	// budget while making the figure meaningful. A cue's start is the running sum
	// of the preceding durations, so each duration is chosen to place the next
	// cue at interval start + its own jitter.
	const cueIntervalTicks = 10 * 90000 // one cue every 10 s
	const jitterTicks = 18 * 90         // ≤ 18 ms
	jitters := make([]int, len(cues))
	for i := range jitters {
		if i > 0 {
			jitters[i] = rng.intn(jitterTicks)
		}
	}
	var subs []Sample
	for i, cue := range cues {
		body := append([]byte{byte(len(cue) >> 8), byte(len(cue))}, []byte(cue)...)
		// This sample starts at interval*i + jitter[i]; the duration carries the
		// difference to the next cue's start.
		var dur int
		if i+1 < len(cues) {
			dur = cueIntervalTicks + jitters[i+1] - jitters[i]
		} else {
			dur = cueIntervalTicks
		}
		subs = append(subs, Sample{Data: body, DurationTicks: uint32(dur), Sync: true})
	}
	tracks := []TrackSpec{
		{ID: id, Handler: "vide", Timescale: 90000, Width: width, Height: 9 * width / 16, Samples: video},
		{ID: id + 10, Handler: "soun", Timescale: 48000, Samples: audio},
		{ID: id + 20, Handler: "subt", Timescale: 90000, Samples: subs, SampleEntry: Tx3gSampleEntry()},
	}
	clip := BuildClip(tracks)
	out := clip.Bytes()
	// Append a top-level chpl box so the chapter list is exercised.
	out = append(out, chplBox(cues, opts.GOP)...)
	descs = append(descs, fmt.Sprintf("rendition %d: %d video frames, %d audio samples, %d subtitle cues, chpl chapters",
		id, len(video), len(audio), len(subs)))
	return out, descs
}

// chplBox builds a Nero chapter list: chapters one GOP apart.
func chplBox(cues []string, gop int) []byte {
	var body []byte
	body = append(body, make([]byte, 4)...) // reserved
	body = append(body, byte(len(cues)))
	for i, cue := range cues {
		start100ns := uint64(i) * 10 * 10_000_000 // one chapter every 10 s
		body = append(body, U64(start100ns)...)
		body = append(body, byte(len(cue)))
		body = append(body, []byte(cue)...)
	}
	return Box("chpl", body)
}

// buildDemoRTP makes an RTP capture that mirrors a rendition's frame timing and
// exercises the recovery cases: duplicates, reorders, a sequence/timestamp wrap,
// recoverable loss (a first transmission is delayed and arrives later inside the
// reorder window), and a small bounded permanent loss.
func buildDemoRTP(opts DemoOptions, rng *demoRand) ([]byte, []string) {
	var descs []string
	const ssrc = 0xdeadbeef
	const startSeq = 65500 // close to the wrap point
	const startTS = (1 << 32) - 40000

	// Build every packet the stream should contain, keyed by sequence.
	type pending struct {
		seq     uint16
		ts      uint32
		marker  bool
		payload []byte
	}
	all := make([]pending, opts.Frames)
	seq := uint16(startSeq)
	ts := uint32(startTS)
	for i := 0; i < opts.Frames; i++ {
		all[i] = pending{seq: seq, ts: ts, marker: i == 0 || i%opts.GOP == 0, payload: bytes4(byte(i), 0xa5, 0x5a, byte(rng.next()))}
		seq++
		ts += 3000
	}

	// Decide delivery per packet: normal, duplicate, reorder, delayed (recovered
	// loss), or permanently lost. Delayed retransmissions stay within the window.
	const window = mediaReorderWindow
	permanentBudget := int(float64(opts.Frames) * opts.Loss * 0.5)
	var datagrams []RTPDatagram
	var delayed []pending // retransmissions to append shortly after their slot
	dupes, reorders, recoveredLoss, permanent := 0, 0, 0, 0
	for i, p := range all {
		if p.payload == nil {
			continue // already emitted by a reorder
		}
		pkt := marshalRTP(ssrc, p.seq, p.ts, p.marker, p.payload)
		// Exhaust delayed retransmissions whose window has arrived.
		for len(delayed) > 0 && int(uint16(all[i].seq-delayed[0].seq)) <= window/2 && i > 0 {
			dp := delayed[0]
			delayed = delayed[1:]
			datagrams = append(datagrams, marshalRTP(ssrc, dp.seq, dp.ts, dp.marker, dp.payload))
		}
		roll := rng.float()
		switch {
		case i > 0 && roll < opts.Loss && permanent < permanentBudget:
			// Permanent loss: never delivered.
			permanent++
		case i > 0 && roll < opts.Loss*2:
			// Recoverable loss: first transmission is delayed past the current
			// point but delivered within the window.
			delayed = append(delayed, p)
			recoveredLoss++
		case i%11 == 5:
			// Duplicate retransmission of the same payload.
			datagrams = append(datagrams, pkt, pkt)
			dupes++
		case i%17 == 7 && i+1 < len(all) && all[i+1].payload != nil:
			// Reorder: emit the next packet before this one, then mark it so the
			// next iteration does not emit it again.
			n := all[i+1]
			datagrams = append(datagrams, marshalRTP(ssrc, n.seq, n.ts, n.marker, n.payload), pkt)
			all[i+1].payload = nil
			reorders++
		default:
			datagrams = append(datagrams, pkt)
		}
	}
	// Flush any remaining delayed retransmissions.
	for _, dp := range delayed {
		datagrams = append(datagrams, marshalRTP(ssrc, dp.seq, dp.ts, dp.marker, dp.payload))
	}
	descs = append(descs, fmt.Sprintf(
		"rtp: %d datagrams, %d duplicates, %d reorders, %d delayed-loss recovered, %d permanent-loss, sequence 65535->0 and timestamp wrap",
		len(datagrams), dupes, reorders, recoveredLoss, permanent))
	return EncodeRTPCapture(datagrams), descs
}

// mediaReorderWindow mirrors media.DefaultReorderWindow for scenario shaping.
const mediaReorderWindow = 64

// marshalRTP writes a minimal RTP v2 packet (12-byte header + payload).
func marshalRTP(ssrc uint32, seq uint16, ts uint32, marker bool, payload []byte) RTPDatagram {
	hdr := make([]byte, 12, 12+len(payload))
	hdr[0] = 0x80 // version 2, no padding/extension/CSRC
	hdr[1] = 96   // dynamic payload type
	if marker {
		hdr[1] |= 0x80
	}
	binary.BigEndian.PutUint16(hdr[2:], seq)
	binary.BigEndian.PutUint32(hdr[4:], ts)
	binary.BigEndian.PutUint32(hdr[8:], ssrc)
	return append(hdr, payload...)
}

// buildTruncatedEmsgClip writes a valid video clip with an emsg whose declared
// message length exceeds the box.
func buildTruncatedEmsgClip(opts DemoOptions, rng *demoRand) []byte {
	clip := buildMinimalVideo(1, opts, rng)
	// Build a v0 emsg that go-mp4 will reject: valid box header but the scheme
	// string is never terminated within the box.
	body := []byte{0, 0, 0, 0}                // version 0, flags
	body = append(body, []byte("urn:bad")...) // no NUL terminator before box end
	return append(clip.Bytes(), Box("emsg", body)...)
}

// buildTruncatedChplClip writes a valid video clip with a chpl whose chapter
// header claims a title longer than the payload.
func buildTruncatedChplClip(opts DemoOptions, rng *demoRand) []byte {
	clip := buildMinimalVideo(7, opts, rng)
	body := []byte{0, 0, 0, 0, 2}           // claims 2 chapters
	body = append(body, make([]byte, 4)...) // one short chapter header, then end
	return append(clip.Bytes(), Box("chpl", body)...)
}

func buildMinimalVideo(id uint32, opts DemoOptions, rng *demoRand) Clip {
	var samples []Sample
	n := 30
	if opts.Frames < n {
		n = opts.Frames
	}
	for i := 0; i < n; i++ {
		samples = append(samples, Sample{Data: bytes4(byte(id), byte(i), 0x01, 0x02), DurationTicks: 3000, Sync: i%opts.GOP == 0})
	}
	return BuildClip([]TrackSpec{
		{ID: id, Handler: "vide", Timescale: 90000, Width: 320, Height: 180, Samples: samples},
	})
}

func bytes3(a, b byte) []byte       { return []byte{a, b, 0x00} }
func bytes4(a, b, c, d byte) []byte { return []byte{a, b, c, d} }

// demoCues returns a cue per 10-second span of the 200-second material.
func demoCues() []string {
	names := []string{
		"Opening titles", "First scene", "Second scene", "Third scene",
		"Interlude", "Climax", "Resolution", "Credits",
	}
	return names
}
