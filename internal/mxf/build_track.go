package mxf

import (
	"crypto/sha256"

	"media-audit/internal/media"
	"media-audit/internal/timebase"
	"media-audit/internal/wire"
)

// BuildTrackFile constructs a deterministic KLV stream and appends SHA-256
// integrity for the sample-table value.
func BuildTrackFile(id uint32, tb timebase.Timebase, duration int64, md media.Metadata, specs []media.SampleSpec) []byte {
	if duration == 0 && len(specs) > 0 {
		last := specs[len(specs)-1]
		duration = last.PTS + int64(last.Duration)
	}
	var b wire.Buffer
	AppendKLV(&b, KeyPartition, []byte("media-audit-op1a"))
	AppendKLV(&b, KeyTrackMeta, media.EncodeTrackMeta(id, tb, duration, md))
	sampleEl := AppendKLV(&b, KeySampleTable, media.EncodeSampleTable(specs))
	integEl := AppendKLV(&b, KeyIntegrity, media.EncodeIntegrity(0, 0, [sha256.Size]byte{}))

	data := b.Bytes()
	start := sampleEl.Offset + int64(sampleEl.HeaderSize)
	sum := sha256.Sum256(data[start:sampleEl.Range.End])
	integ := media.EncodeIntegrity(start, sampleEl.Range.End, sum)
	integStart := integEl.Offset + int64(integEl.HeaderSize)
	copy(data[integStart:integEl.Range.End], integ)
	return data
}
