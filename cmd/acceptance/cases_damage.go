package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"

	"mediacheck/internal/asset"
	"mediacheck/internal/cache"
	"mediacheck/internal/derive"
	rtpx "mediacheck/internal/rtp"
	"mediacheck/internal/timebase"
	"mediacheck/internal/track"
	"mediacheck/internal/wav"
)

func canonicalError(err error) *timebase.Error {
	var te *timebase.Error
	if errors.As(err, &te) {
		return te
	}
	return nil
}

func caseDamage(out string) Report {
	dir := filepath.Join(out, "damage")
	rep := newReport("damage", dir)
	fixtureDir := filepath.Join(dir, "fixtures")
	if err := mkdirAll(fixtureDir); err != nil {
		rep.Error = err.Error()
		return rep
	}

	f := genTone(toneSpec{SampleRate: 48000, Channels: 2, Frames: 4800, Frequency: 392})
	rep.SourceSHA = asset.DigestBytes(f.WAV)
	badRIFF := badRIFFLengthWAV(f.WAV)
	partial := partialFrameWAV()
	_ = cache.AtomicWriteFile(filepath.Join(fixtureDir, "bad-riff-length.wav"), badRIFF, 0o644)
	_ = cache.AtomicWriteFile(filepath.Join(fixtureDir, "partial-frame.wav"), partial, 0o644)

	_, riffErr := wav.OpenAt(bytes.NewReader(badRIFF), int64(len(badRIFF)))
	riffTE := canonicalError(riffErr)
	if riffTE == nil || riffTE.Code != timebase.CodeRIFF || riffTE.Offset != 4 {
		rep.Error = "bad RIFF length did not report stable riff_header at offset 4"
		rep.Details["raw_error"] = riffErr.Error()
		return rep
	}
	_, partialErr := wav.OpenAt(bytes.NewReader(partial), int64(len(partial)))
	partialTE := canonicalError(partialErr)
	if partialTE == nil || partialTE.Code != timebase.CodeAlignment || partialTE.Offset < 0 {
		rep.Error = "partial frame did not report frame_alignment with offset"
		rep.Details["raw_error"] = partialErr.Error()
		return rep
	}
	rep.BadOffset = partialTE.Offset
	rep.Details["riff_bad_offset"] = riffTE.Offset
	rep.Details["partial_error"] = partialTE.Error()

	packets := packetize(f, 0, packetFrames)
	arrivals := shuffledWithDuplicates(removeSequences(packets, 10, 11, 12))
	ledger := newGapLedger("cam-damage", uint32(f.Rate))
	rb, err := rtpx.NewReorderer(rtpx.Config{
		Track: "cam-damage", SampleRate: uint32(f.Rate), StartSequence: 0,
		Window: 64, SamplesPerPacket: packetFrames, Ledger: ledger,
	})
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	var timed []rtpx.TimedPacket
	for _, p := range arrivals {
		raw, mErr := p.Marshal()
		if mErr != nil {
			rep.Error = mErr.Error()
			return rep
		}
		res, pErr := rb.PushRaw(raw)
		if pErr != nil {
			rep.Error = pErr.Error()
			return rep
		}
		timed = append(timed, res.Ready...)
	}
	rest, err := rb.Flush()
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	timed = append(timed, rest...)
	lateRes, err := rb.Push(packets[10])
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	if !lateRes.Late {
		rep.Error = "late missing packet was not classified outside window"
		return rep
	}

	tr, err := track.New("cam-damage", f.Rate, f.Ch, f.Bits, nil)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	for _, tp := range timed {
		if err := tr.AppendPCM(tp.StartSample, tp.Packet.Payload, f.Bits, 0); err != nil {
			rep.Error = err.Error()
			return rep
		}
	}
	tr.AddGaps(ledger.Gaps())
	rep.GapCount = ledger.Count()
	if rep.GapCount < 1 {
		rep.Error = "damage case recorded no gaps"
		return rep
	}

	// A full-range fragment must fail the gap recheck; no bytes are published.
	if _, err := derive.BuildFragment(tr, 0, int64(f.Frames), 44, int64(len(f.PCM)),
		derive.BuildOptions{OutputBits: 16, Sequence: 1}); err == nil {
		rep.Error = "gapped damage input unexpectedly produced a complete fragment"
		return rep
	} else if !timebase.IsCode(err, timebase.CodeFragment) {
		rep.Error = "gapped fragment failed with unexpected code: " + err.Error()
		return rep
	}

	gapJSON, _ := json.MarshalIndent(ledger.Gaps(), "", "  ")
	_ = cache.AtomicWriteFile(filepath.Join(dir, "gaps.json"), gapJSON, 0o644)
	rep.Fragments = 0
	rep.Segments = 0
	rep.Details["missing_packet_events"] = ledger.MissingPackets()
	rep.Details["ordered_packets"] = len(timed)
	rep.Details["late_sequence"] = packets[10].SequenceNumber
	rep.OK = true
	return rep
}
