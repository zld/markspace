package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"media-check/internal/damage"
	"media-check/internal/stream"
)

// digestStructure renders the structural report into a canonical textual form
// and hashes it. The same seed/damage must always produce the same digest.
func digestStructure(rep *stream.Report) string {
	var sb strings.Builder
	for _, tr := range rep.Tracks {
		sb.WriteString(strconv.Itoa(int(tr.TrackID)))
		sb.WriteString(":")
		sb.WriteString(string(tr.Kind))
		sb.WriteString(":samples=")
		sb.WriteString(strconv.Itoa(len(tr.Samples)))
		sb.WriteString(":keys=")
		for _, k := range tr.KeyIndices {
			sb.WriteString(strconv.Itoa(int(k)))
			sb.WriteString(",")
		}
		sb.WriteString(":range=")
		sb.WriteString(strconv.FormatInt(tr.SampleTimeRange[0].Millis(), 10))
		sb.WriteString("-")
		sb.WriteString(strconv.FormatInt(tr.SampleTimeRange[1].Millis(), 10))
		sb.WriteString(";")
	}
	return hashString(sb.String())
}

// digestSegments hashes the canonical damage segment digest so repeat runs
// can assert byte-identical localization output.
func digestSegments(segs []damage.Segment) string {
	summaries := damage.Summarize(segs)
	var sb strings.Builder
	for _, s := range summaries {
		sb.WriteString(strconv.Itoa(int(s.TrackID)))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatInt(s.StartMillis, 10))
		sb.WriteString("-")
		sb.WriteString(strconv.FormatInt(s.EndMillis, 10))
		sb.WriteString("|off=")
		sb.WriteString(strconv.FormatInt(s.FirstErrOffset, 10))
		sb.WriteString("|seq=")
		sb.WriteString(strconv.FormatUint(s.SeqStart, 10))
		sb.WriteString("-")
		sb.WriteString(strconv.FormatUint(s.SeqEnd, 10))
		sb.WriteString("|")
		sb.WriteString(s.Codes)
		sb.WriteString(";")
	}
	return hashString(sb.String())
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
