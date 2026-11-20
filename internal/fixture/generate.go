// Package fixture generates deterministic MPEG-TS sample streams for tests
// and the demo. No external media is read; every packet is synthesized.
package fixture

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Scenario names understood by Generate.
const (
	ScenarioBaseline    = "baseline"
	ScenarioUpdatesLoss = "updates-loss"
	ScenarioCorrupt     = "corrupt"
	ScenarioEverything  = "all"
)

// KnownScenarios lists the accepted --scenario values.
var KnownScenarios = []string{ScenarioBaseline, ScenarioUpdatesLoss, ScenarioCorrupt, ScenarioEverything}

const (
	patPID     uint16 = 0x0000
	pmt1PID    uint16 = 0x0100
	pmt2PID    uint16 = 0x0200
	video1PID  uint16 = 0x0101
	audio1PID  uint16 = 0x0102
	video2PID  uint16 = 0x0201
	audio2PID  uint16 = 0x0202
	unknownPID uint16 = 0x0300
	tsid       uint16 = 0x0042
)

// periodic PMT descriptors long enough to span two TS packets: 170 body bytes
// plus an 8-byte syntax header exceed the 184-byte payload.
func longVideoDesc() []byte { return padDescriptor(180) }

func pmtFor(program uint16, version uint8, extra bool) []byte {
	switch program {
	case 1:
		s := []stream{
			{Type: 0x1b, PID: video1PID},
			{Type: 0x0f, PID: audio1PID},
		}
		if extra {
			s = append(s, stream{Type: 0x06, PID: 0x0103})
		}
		return pmtSection(1, version, true, 0, 0, video1PID, s)
	default:
		s := []stream{
			{Type: 0x02, PID: video2PID},
			{Type: 0x03, PID: audio2PID},
			{Type: 0x99, PID: unknownPID}, // unknown stream type
		}
		return pmtSection(2, version, true, 0, 0, video2PID, s)
	}
}

func patV0() []byte {
	return patSection(tsid, 0, true, 0, 0, [][2]uint16{{0, 0x0010}, {1, pmt1PID}, {2, pmt2PID}})
}

func patV1() []byte {
	return patSection(tsid, 1, true, 0, 0, [][2]uint16{{0, 0x0010}, {1, pmt1PID}, {2, pmt2PID}})
}

// videoPayload produces deterministic, non-repeating payload bytes per PID.
func videoPayload(pid uint16, n int64) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((int64(pid) + n + int64(i)*7) & 0xff)
	}
	return b
}

// Generate writes a scenario to out. A temporary file is used and renamed on
// success so a failed run never leaves a partial fixture at the target path.
func Generate(out, scenario string) (err error) {
	if scenario != ScenarioBaseline && scenario != ScenarioUpdatesLoss &&
		scenario != ScenarioCorrupt && scenario != ScenarioEverything {
		return fmt.Errorf("fixture: unknown scenario %q (want one of %v)", scenario, KnownScenarios)
	}
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fixture: creating output directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tsinspect-*.tmp")
	if err != nil {
		return fmt.Errorf("fixture: creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err = build(tmp, scenario); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("fixture: closing temporary file: %w", err)
	}
	if err = os.Rename(tmpName, out); err != nil {
		return fmt.Errorf("fixture: publishing fixture: %w", err)
	}
	return nil
}

// lossPendingTable returns a two-section PMT split so section 0 completes
// before section 1 is cut short by packet loss.
func lossPendingTable() (complete, partialA, partialB []byte) {
	complete = pmtSection(2, 7, true, 0, 1, video2PID, []stream{
		{Type: 0x02, PID: video2PID, Desc: longVideoDesc()},
	})
	sec := pmtSection(2, 7, true, 1, 1, video2PID, []stream{
		{Type: 0x03, PID: audio2PID, Desc: longVideoDesc()},
	})
	data := append([]byte{0x00}, sec...)
	return complete, data[:payloadSize], data[payloadSize:]
}

func build(w io.Writer, scenario string) error {
	m := newMux(w)
	base := scenario == ScenarioBaseline
	updates := scenario == ScenarioUpdatesLoss || scenario == ScenarioEverything
	corrupt := scenario == ScenarioCorrupt || scenario == ScenarioEverything

	// --- Opening tables, repeated so the decoder can lock on. ---
	for rep := 0; rep < 3; rep++ {
		m.section(patPID, patV0())
		m.section(pmt1PID, pmtFor(1, 0, false))
		m.section(pmt2PID, pmtFor(2, 0, false))
		emitMedia(m, 6)
	}
	if base {
		m.lose(video1PID, 1) // provoke one continuity error
		emitMedia(m, 2)
		m.tail(37)
		return m.err
	}

	// --- Single version update; every later repeat reuses version 1. ---
	m.section(patPID, patV1())
	// Program 1 gains a third stream via a long, two-packet PMT.
	long := pmtSection(1, 1, true, 0, 0, video1PID, []stream{
		{Type: 0x1b, PID: video1PID, Desc: longVideoDesc()},
		{Type: 0x0f, PID: audio1PID},
		{Type: 0x06, PID: 0x0103},
	})
	m.section(pmt1PID, long)
	m.section(pmt2PID, pmtFor(2, 1, false))
	emitMedia(m, 4)

	// A future claim (current_next=0) must never take effect.
	m.section(patPID, patSection(tsid, 2, false, 0, 0, [][2]uint16{{1, pmt1PID}}))
	m.section(pmt1PID, pmtSection(1, 2, false, 0, 0, video1PID,
		[]stream{{Type: 0x1b, PID: 0x0999}}))

	if updates {
		// Packet loss midway through a two-section PMT: section 0 is already
		// buffered, so the table must be abandoned, not completed from stale
		// bytes or from the later repeat of section 1.
		complete, partialA, partialB := lossPendingTable()
		m.section(pmt2PID, complete)
		m.send(pmt2PID, partialA, true)
		m.lose(pmt2PID, 1)
		m.send(pmt2PID, partialB, false)
		m.section(pmt2PID, pmtFor(2, 1, false))

		// Lose payload packets in the middle of program 2's media.
		emitMedia(m, 1)
		m.lose(video2PID, 1)
		emitMedia(m, 1)
		m.lose(audio2PID, 2)
		emitMedia(m, 4)

		// Signalled discontinuity: must not be a continuity error.
		m.discontinuity(video1PID, videoPayload(video1PID, payloadSize))
		emitMedia(m, 3)

		// Duplicate packet and an adaptation-only packet with PCR.
		m.duplicate(audio1PID, videoPayload(audio1PID, payloadSize))
		m.sendAdaptationOnly(video1PID, 90000)
		emitMedia(m, 3)

		// A second incomplete version at end of stream stays pending: the PAT
		// announces two sections but only section 0 is sent, and the PMT is
		// likewise declared as two sections with only section 0 present.
		m.section(patPID, patSection(tsid, 3, true, 0, 1, [][2]uint16{{1, pmt1PID}}))
		m.section(pmt1PID, pmtSection(1, 3, true, 0, 1, video1PID,
			[]stream{{Type: 0x1b, PID: video1PID}, {Type: 0x0f, PID: audio1PID}}))
	}
	if corrupt {
		// Transport error on a PMT PID, then a bad-CRC PMT, then recovery.
		m.sendTEI(pmt2PID, pmtFor(2, 1, false))
		m.section(pmt2PID, corruptCRC(pmtSection(2, 5, true, 0, 0, video2PID,
			[]stream{{Type: 0x02, PID: 0x0abc}})))
		m.lose(video1PID, 1)
		emitMedia(m, 2)
		m.section(patPID, patV1())
		m.section(pmt1PID, pmtFor(1, 1, true))
		// A transport error before the repeat keeps the recovery honest: the
		// counter baseline must resync rather than report a false jump.
		m.sendTEI(pmt2PID, videoPayload(pmt2PID, 8))
		m.section(pmt2PID, pmtFor(2, 1, false))
		emitMedia(m, 4)
	}
	m.tail(41)
	return m.err
}

// emitMedia writes a small deterministic burst of video and audio packets for
// both programs.
func emitMedia(m *mux, n int) {
	for i := 0; i < n; i++ {
		m.send(video1PID, videoPayload(video1PID, payloadSize), false)
		m.send(audio1PID, videoPayload(audio1PID, payloadSize), false)
		m.send(video2PID, videoPayload(video2PID, payloadSize), false)
		m.send(audio2PID, videoPayload(audio2PID, payloadSize), false)
	}
}

// PATOnlyBytes returns a tiny stream whose PAT maps program 1 to a PMT PID
// that never carries a table. It is exported for negative slicing tests.
func PATOnlyBytes() []byte {
	var buf bytes.Buffer
	m := newMux(&buf)
	for i := 0; i < 3; i++ {
		m.section(patPID, patV0())
	}
	m.tail(5)
	return buf.Bytes()
}
