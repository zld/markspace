package main

import (
	"bytes"
	"fmt"
	"os"

	"mediacore"
	"mediacore/audit"
	"mediacore/buffer"
	"mediacore/index"
	"mediacore/syncer"
	"mediacore/timeline"
)

// driftBoundPPM is the auto-alignment ceiling; the demo injects 180 ppm which
// stays inside it, while the corrupt-fixture suite also exercises an
// over-bound drift to produce a classified drift error.
const driftBoundPPM int64 = 300

// auditHashes returns per-segment hex hashes in time order.
func auditHashes(e *timeline.Engine) []string {
	return audit.HashAll(e.Segments())
}

// exerciseCorruptFixtures feeds deliberately broken inputs through the strict
// parsers and returns how many were correctly rejected. The main pipeline
// keeps running because each failure is isolated to its own fixture.
func exerciseCorruptFixtures(goodMP4 []byte, elog *errorLog) int {
	before := len(elog.cats)
	trackFor := func(uint32) mediacore.TrackID { return mediacore.TrackVideo1 }

	// 1. Truncated MP4 (declared box runs past EOF).
	if len(goodMP4) > 16 {
		_, err := index.ParseMP4(trackFor, bytesReader(goodMP4[:len(goodMP4)/2]))
		elog.record(err)
	}
	// 2. Illegal WAV channel layout.
	badWAV := wavWithBadChannels()
	_, err := buffer.ParseWAV(mediacore.TrackAudio1, bytesReader(badWAV))
	elog.record(err)

	// 3. Drift beyond the configured ppm bound.
	over := syncerOverBound()
	elog.record(over.StateError())

	// 4. A malformed RTP packet must be rejected as corrupt, not crash.
	rtpParser := buffer.NewRTPParser(mediacore.TrackAudio1)
	if _, _, rerr := rtpParser.Parse([]byte{0x80, 0x00}, 0); rerr != nil {
		elog.record(rerr)
	}

	return len(elog.cats) - before
}

// wavWithBadChannels builds a structurally valid WAV whose channel count is
// zero, which the strict parser must reject as an illegal layout.
func wavWithBadChannels() []byte {
	// Reuse the writer with an invalid layout by hand-crafting the header.
	raw := make([]byte, 44)
	copy(raw[0:4], []byte("RIFF"))
	copy(raw[8:12], []byte("WAVE"))
	copy(raw[12:16], []byte("fmt "))
	putLE32(raw[16:20], 16)
	putLE16(raw[20:22], 1) // PCM
	putLE16(raw[22:24], 0) // zero channels -> illegal
	putLE32(raw[24:28], 8000)
	putLE32(raw[28:32], 8000*0*2)
	putLE16(raw[32:34], 0*2)
	putLE16(raw[34:36], 16)
	copy(raw[36:40], []byte("data"))
	putLE32(raw[40:44], 0)
	putLE32(raw[4:8], uint32(36))
	return raw
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func putLE16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// syncerOverBound builds an estimator whose observed drift exceeds the ppm
// ceiling, yielding a classified CatDrift state error.
func syncerOverBound() *syncer.DriftEstimator {
	est := syncer.NewDriftEstimator(mediacore.TrackAudio1, driftBoundPPM)
	for i := int64(1); i <= 20; i++ {
		master := i * 100 * mediacore.Millisecond
		device := int64(float64(master) * 1.001) // 1000 ppm > 300 bound
		est.Observe(syncer.Anchor{Device: device, Master: master})
	}
	return est
}

func main() {
	args := os.Args[1:]
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	m, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(m.String())
	if !m.pass() {
		os.Exit(1)
	}
	os.Exit(0)
}
