package fixture

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/ingest"
	"mediapipeline/internal/merge"
)

// IndexTimeBaseNum/Den give a 90 kHz clock; ticks of KeyStride*3000 convert
// uniquely to millisecond units, so clean samples never raise time-base faults.
const (
	indexTimeBaseNum = 1
	indexTimeBaseDen = 90000
	// ticksPerUnit keeps every key-frame tick an exact 90 kHz multiple.
	ticksPerUnit = 3000
)

// Sample is a fully generated, optionally damaged, test fixture.
type Sample struct {
	Codec      ingest.Codec
	Seed       int64
	Stream     []byte // stream bytes after byte-level injections
	FullStream []byte // undamaged stream, used to prove resume equivalence
	Payloads   [][]byte
	IndexBytes []byte   // serialised key-frame index container
	Packets    [][]byte // RTP feed after transport injections
	SSRC       uint32
	Versions   []merge.Version
	Bandwidth  BandwidthProfile

	// Damage bookkeeping, exact positions the injections touched.
	TruncateOffset int64 // -1 when not injected
	IndexOOBOffset int64 // byte offset of the OOB entry, -1 when not injected
	LostSequences  []uint16
	DuplicateCount int
	Reordered      bool
}

// Generate builds a sample for codec/seed and applies the named injections.
// Injections are applied in a fixed order so results are deterministic.
func Generate(codec ingest.Codec, seed int64, injections []string) (*Sample, error) {
	if err := validateCodec(codec); err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(injections))
	for _, name := range injections {
		if !knownInjection(name) {
			return nil, errUnknownInjection(name)
		}
		enabled[name] = true
	}

	stream, payloads := buildUnits(codec, seed)
	s := &Sample{
		Codec: codec, Seed: seed,
		Stream: append([]byte(nil), stream...), FullStream: stream,
		Payloads: payloads, SSRC: ssrc,
		Bandwidth:      DefaultBandwidth(),
		TruncateOffset: -1, IndexOOBOffset: -1,
	}

	indexBytes, oobOff, err := buildIndex(codec, payloads, enabled[InjectIndexOOB])
	if err != nil {
		return nil, err
	}
	s.IndexBytes = indexBytes
	s.IndexOOBOffset = oobOff

	if enabled[InjectTruncate] {
		applyTruncate(s)
	}
	s.Versions = buildVersions(s)
	fr := buildFeed(payloads, enabled)
	s.Packets = fr.packets
	s.LostSequences = fr.lost
	s.DuplicateCount = fr.dups
	s.Reordered = fr.reordered
	if enabled[InjectBandwidthDrop] {
		s.Bandwidth.NormalBps = DefaultBandwidth().NormalBps
		s.Bandwidth.DroppedBps = DefaultBandwidth().DroppedBps
	}
	return s, nil
}

// buildIndex serialises a key-frame index referencing the key units, optionally
// appending an out-of-bounds entry.  The returned offset is the entry's byte
// position inside the index box payload (the position the parser faults at).
func buildIndex(codec ingest.Codec, payloads [][]byte, withOOB bool) ([]byte, int64, error) {
	base := clock.MustRational(indexTimeBaseNum, indexTimeBaseDen)
	var entries []ingest.IndexEntry
	off := int64(0)
	for idx, body := range payloads {
		if idx%KeyStride != 0 {
			off += unitFraming(codec, body)
			continue
		}
		entries = append(entries, ingest.IndexEntry{
			UnitIndex: uint32(idx),
			Tick:      int64(idx * ticksPerUnit),
			Offset:    off,
			Size:      uint32(len(body)),
			TimeBase:  base,
		})
		off += unitFraming(codec, body)
	}
	f := &ingest.IndexFile{Codec: codec, Base: base, Entries: entries}
	if !withOOB {
		good, err := ingest.WriteIndex(f)
		if err != nil {
			return nil, -1, err
		}
		return good, -1, nil
	}
	// Append one malformed 24-byte entry referencing unit 9999.  go-mp4 still
	// frames the box correctly; its unit index is what the parser must reject.
	oob := ingest.IndexEntry{UnitIndex: 9999, Tick: 0, Offset: 0, Size: 1, TimeBase: base}
	f.Entries = append(entries, oob)
	bad, err := ingest.WriteIndex(f)
	if err != nil {
		return nil, -1, err
	}
	// Entry offset inside the box payload: header(16) + 24 bytes per good entry.
	oobOff := int64(16 + len(entries)*24)
	return bad, oobOff, nil
}

// unitFraming reports how many bytes one unit occupies in the framed stream.
func unitFraming(codec ingest.Codec, body []byte) int64 {
	if codec == ingest.CodecAV1 {
		return int64(1 + len(leb128(uint64(len(body)))) + len(body))
	}
	return int64(4 + len(body))
}
