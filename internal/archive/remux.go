// Package archive performs lossless remuxing: compressed sample bytes, track
// metadata and sample order are preserved through a fragmented-MP4 round
// trip. Every remux is verified by re-demuxing and comparing sample
// fingerprints; a mismatch is reported as a failure, never as a success.
package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/abema/go-mp4"

	"mediarc/internal/demux"
	"mediarc/internal/model"
)

// RemuxReport is the verified outcome of one remux attempt.
type RemuxReport struct {
	Output          []byte
	TrackCount      int
	SampleCount     int
	InputHash       string
	OutputHash      string
	Lossless        bool
	Error           *model.MediaError
}

// handlerFor maps a model codec back to the MP4 handler type used by the
// fixture writer. Unknown codecs still remux losslessly; only mapping rejects
// them.
func handlerFor(c model.Codec) string {
	switch c {
	case model.CodecAVC1:
		return "vide"
	case model.CodecMP4A:
		return "soun"
	default:
		return "hint"
	}
}

// trackHash fingerprints the compressed sample bytes in decode order,
// including relative timing (DTS expressed against the first sample),
// durations and sync flags. Relative time is the correct lossless contract
// for fragmented MP4: each fragment re-bases baseMediaDecodeTime onto its
// own first sample while preserving every inter-sample distance and byte.
func trackHash(tr model.Track) string {
	h := sha256.New()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{'\n'})
	}
	write(string(tr.Codec))
	write(itoa32(tr.Timescale))
	var base uint64
	if len(tr.Samples) > 0 {
		base = tr.Samples[0].Dts
	}
	for _, s := range tr.Samples {
		write(itoa64(int64(s.Dts - base)))
		write(itoa32(s.Duration))
		if s.Sync {
			write("1")
		} else {
			write("0")
		}
		_, _ = h.Write(s.Payload)
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Remux writes the tracks into one self-contained fragmented MP4 (each track
// becomes one fragment sequence) and immediately verifies byte-exact sample
// recovery. A failed verification returns a classified report instead of an
// error-free container.
func Remux(fileName string, tracks []model.Track) RemuxReport {
	report := RemuxReport{Lossless: true}
	if len(tracks) == 0 {
		report.Lossless = false
		report.Error = model.New(model.KindRemuxMismatch, fileName, "no tracks to remux")
		return report
	}

	inHashes := make([]string, len(tracks))
	all := make([]byte, 0, 256)
	for i, tr := range tracks {
		inHashes[i] = trackHash(tr)
		report.SampleCount += len(tr.Samples)
		frags := make([][]demux.FixtureSample, 1)
		for _, s := range tr.Samples {
			frags[0] = append(frags[0], demux.FixtureSample{
				Dts: s.Dts, Duration: s.Duration,
				Payload: append([]byte(nil), s.Payload...), Sync: s.Sync,
			})
		}
		// One fMP4 per track keeps each track's timescale and metadata exact;
		// the buffers are concatenated as a self-contained archive bundle.
		data, err := demux.WriteFragmentedMP4(tr.Timescale, tr.ID, handlerFor(tr.Codec), frags)
		if err != nil {
			report.Lossless = false
			report.Error = model.New(model.KindRemuxMismatch, fileName, "mp4 write failed: "+err.Error())
			return report
		}
		all = append(all, data...)
	}
	report.Output = all
	report.TrackCount = len(tracks)
	report.InputHash = combine(inHashes)

	// Verify by re-demuxing every embedded fMP4 bundle member.
	outTracks, err := extractTracks(fileName, all)
	if err != nil {
		report.Lossless = false
		report.Error = err
		return report
	}
	if len(outTracks) != len(tracks) {
		report.Lossless = false
		report.Error = model.New(model.KindRemuxMismatch, fileName,
			"track count changed: in "+itoa(len(tracks))+" out "+itoa(len(outTracks)))
		return report
	}
	outHashes := make([]string, len(outTracks))
	for i, tr := range outTracks {
		outHashes[i] = trackHash(tr)
	}
	report.OutputHash = combine(outHashes)
	if report.InputHash != report.OutputHash {
		report.Lossless = false
		report.Error = model.New(model.KindRemuxMismatch, fileName,
			"sample fingerprint changed through remux")
	}
	return report
}

// extractTracks walks a concatenation of standalone fragmented MP4s, demuxing
// each top-level ftyp/moov region separately.
func extractTracks(fileName string, data []byte) ([]model.Track, *model.MediaError) {
	var out []model.Track
	r := bytes.NewReader(data)
	pos := 0
	for pos < len(data) {
		if _, err := r.Seek(int64(pos), io.SeekStart); err != nil {
			return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(pos), "", err.Error())
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(pos), "", "archive box unreadable: "+err.Error())
		}
		end := pos + int(bi.Size)
		if end <= pos || end > len(data) {
			return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(pos), "", "box size out of range")
		}
		if bi.Type != mp4.BoxTypeFtyp() {
			// Skip non-bundle leading boxes defensively.
			pos = end
			continue
		}
		// One bundle member spans until the next ftyp or end of buffer.
		memberEnd := end
		for memberEnd < len(data) {
			if _, err := r.Seek(int64(memberEnd), io.SeekStart); err != nil {
				return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(memberEnd), "", err.Error())
			}
			next, err := mp4.ReadBoxInfo(r)
			if err != nil {
				return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(memberEnd), "", err.Error())
			}
			if next.Type == mp4.BoxTypeFtyp() {
				break
			}
			nextEnd := memberEnd + int(next.Size)
			if nextEnd <= memberEnd || nextEnd > len(data) {
				return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(memberEnd), "", "box size out of range")
			}
			memberEnd = nextEnd
		}
		tracks, derr := demux.NewMP4Demuxer(fileName).Demux(data[pos:memberEnd])
		if derr != nil {
			return nil, model.AtOffset(model.KindRemuxMismatch, fileName, int64(pos), "", derr.Error())
		}
		out = append(out, tracks...)
		pos = memberEnd
	}
	return out, nil
}

func combine(hashes []string) string {
	h := sha256.New()
	for _, s := range hashes {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	n := len(b)
	for x := v; x > 0; x /= 10 {
		n--
		b[n] = byte('0' + x%10)
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

func itoa32(v uint32) string { return itoa(int(v)) }
func itoa64(v int64) string  { return itoa(int(v)) }
