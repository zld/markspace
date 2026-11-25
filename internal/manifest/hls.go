package manifest

import (
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mediacore/internal/clock"
	"mediacore/internal/diagnostic"
	"mediacore/internal/workspace"
)

type HLS struct {
	ManifestPath string
	WorkRoot     string
	Entries      []Entry
}

// ParseHLS parses a media playlist. Discontinuities are recorded but never reset
// the continuous unified program clock.
func ParseHLS(path, workRoot string) (*HLS, error) {
	return ParseHLSWithExisting(path, workRoot, nil)
}

// ParseHLSWithExisting suppresses original sequence numbers already present in existing.
func ParseHLSWithExisting(path, workRoot string, existing []Entry) (*HLS, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, diagnostic.Wrap(nil, diagnostic.Location{Stage: "hls", ManifestPath: path, ByteOffset: 0}, err)
	}
	h := &HLS{ManifestPath: path, WorkRoot: workRoot}
	manifestDir := filepath.Dir(path)
	rendition := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	mediaSequence := 0
	seen := map[int]struct{}{}
	for _, e := range existing {
		if e.System == "hls" && e.Representation == rendition {
			seen[e.OriginalSequence] = struct{}{}
		}
	}

	cursor := int64(0)
	segmentIndex := 0
	var pendingDurSec string
	var pendingLineOffset int64
	var initPath string
	discontinuity := false
	explicitSeq := -1
	var lastPDT time.Time
	havePDT := false
	offset := int64(0)
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		lineStart := offset
		offset += int64(len(rawLine)) + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA-SEQUENCE:") {
			v := strings.TrimPrefix(trimmed, "#EXT-X-MEDIA-SEQUENCE:")
			n, perr := strconv.Atoi(strings.TrimSpace(v))
			if perr != nil || n < 0 {
				return nil, h.err(diagnostic.ErrOutOfRange, segmentIndex, lineStart, "invalid EXT-X-MEDIA-SEQUENCE: "+v)
			}
			mediaSequence = n
			continue
		}
		if trimmed == "#EXT-X-DISCONTINUITY" {
			discontinuity = true
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-ORIGINAL-SEQUENCE:") {
			v := strings.TrimPrefix(trimmed, "#EXT-X-ORIGINAL-SEQUENCE:")
			n, perr := strconv.Atoi(strings.TrimSpace(v))
			if perr != nil {
				return nil, h.err(diagnostic.ErrOutOfRange, segmentIndex, lineStart, "invalid original sequence: "+v)
			}
			explicitSeq = n
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-PROGRAM-DATE-TIME:") {
			v := strings.TrimPrefix(trimmed, "#EXT-X-PROGRAM-DATE-TIME:")
			ts, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
			if perr != nil {
				return nil, h.err(nil, segmentIndex, lineStart, "invalid PROGRAM-DATE-TIME: "+perr.Error())
			}
			if havePDT && ts.Before(lastPDT) {
				return nil, h.err(diagnostic.ErrTimeRegression, segmentIndex, lineStart, "PROGRAM-DATE-TIME moved backwards")
			}
			lastPDT = ts
			havePDT = true
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MAP:") {
			attrs := parseTagAttributes(strings.TrimPrefix(trimmed, "#EXT-X-MAP:"))
			uri := strings.Trim(attrs["URI"], `"`)
			if uri == "" {
				return nil, h.err(diagnostic.ErrMissingDuration, segmentIndex, lineStart, "EXT-X-MAP has no URI")
			}
			p, perr := workspace.Resolve(workRoot, manifestDir, uri)
			if perr != nil {
				return nil, diagnostic.Wrap(perr, h.loc(segmentIndex, mediaSequence+segmentIndex, lineStart), perr)
			}
			initPath = p
			continue
		}
		if strings.HasPrefix(trimmed, "#EXTINF:") {
			rest := strings.TrimPrefix(trimmed, "#EXTINF:")
			parts := strings.SplitN(rest, ",", 2)
			pendingDurSec = strings.TrimSpace(parts[0])
			pendingLineOffset = lineStart
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// URI line.
		if pendingDurSec == "" {
			return nil, h.err(diagnostic.ErrMissingDuration, segmentIndex, lineStart, "media URI without preceding EXTINF")
		}
		durU, perr := parseEXTINFDuration(pendingDurSec)
		if perr != nil {
			return nil, h.err(diagnostic.ErrMissingDuration, segmentIndex, pendingLineOffset, perr.Error())
		}
		seq := mediaSequence + segmentIndex
		if explicitSeq >= 0 {
			seq = explicitSeq
		}
		entryIndex := segmentIndex
		segmentIndex++
		explicitSeq = -1

		startU := cursor
		endU, err := clock.Add(startU, durU)
		if err != nil {
			return nil, diagnostic.Wrap(err, h.loc(entryIndex, seq, lineStart), err.Error())
		}
		cursor = endU

		if _, dup := seen[seq]; dup {
			discontinuity = false
			pendingDurSec = ""
			continue
		}
		seen[seq] = struct{}{}
		mediaPath, err := workspace.Resolve(workRoot, manifestDir, trimmed)
		if err != nil {
			return nil, diagnostic.Wrap(err, h.loc(entryIndex, seq, lineStart), err.Error())
		}
		e := Entry{
			StableKey:           "hls:" + rendition + ":" + strconv.Itoa(seq),
			System:              "hls",
			Manifest:            path,
			Representation:      rendition,
			EntryIndex:          entryIndex,
			OriginalSequence:    seq,
			InitPath:            initPath,
			MediaPath:           mediaPath,
			MediaURI:            trimmed,
			SourceTimescale:     clock.UnifiedTimescale,
			SourceStart:         startU,
			SourceDuration:      durU,
			ProgramStartU:       startU,
			ProgramEndU:         endU,
			ReferenceStartU:     0,
			DeviationU:          startU,
			Aligned:             true,
			DiscontinuityBefore: discontinuity,
		}
		if startU != 0 {
			// HLS's own continuous clock is the reference; deviation is zero by construction.
			e.DeviationU = 0
		}
		h.Entries = append(h.Entries, e)
		discontinuity = false
		pendingDurSec = ""
	}
	if len(h.Entries) == 0 {
		return nil, h.err(diagnostic.ErrMissingDuration, -1, 0, "HLS playlist contains no segments")
	}
	if err := ValidateMonotonic(h.Entries); err != nil {
		return nil, err
	}
	return h, nil
}

func parseEXTINFDuration(s string) (int64, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		// big.Rat does not accept all plain decimals in older forms; parse manually.
		if !strings.ContainsAny(s, ".") {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return 0, err
			}
			r = big.NewRat(n, 1)
		} else {
			parts := strings.SplitN(s, ".", 2)
			n, err := strconv.ParseInt(parts[0]+parts[1], 10, 64)
			if err != nil {
				return 0, err
			}
			den := int64(1)
			for i := 0; i < len(parts[1]); i++ {
				den *= 10
			}
			if strings.HasPrefix(s, "-") {
				// concatenation already includes sign only in parts[0], so keep as parsed.
			}
			r = big.NewRat(n, den)
		}
	}
	if r.Sign() < 0 {
		return 0, diagnostic.ErrTimeRegression
	}
	num := r.Num()
	if !num.IsInt64() {
		return 0, diagnostic.ErrOverflow
	}
	den := r.Denom()
	if !den.IsInt64() || den.Int64() == 0 {
		return 0, diagnostic.ErrZeroDenominator
	}
	return clock.RationalTicks(clock.Rational{Num: num.Int64(), Den: den.Int64()}, clock.UnifiedTimescale)
}

func (h *HLS) loc(index, seq int, offset int64) diagnostic.Location {
	return diagnostic.Location{
		Stage:          "hls",
		ManifestPath:   h.ManifestPath,
		Representation: filepath.Base(h.ManifestPath),
		EntryIndex:     index,
		Sequence:       seq,
		ByteOffset:     offset,
	}
}

func (h *HLS) err(kind error, index int, offset int64, msg string) error {
	return diagnostic.New(kind, h.loc(index, 0, offset), msg)
}

func parseTagAttributes(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}

// MergeEntries appends only entries whose system representation/sequence key is new.
func MergeEntries(existing, incoming []Entry) []Entry {
	seen := map[string]struct{}{}
	for _, e := range existing {
		seen[e.StableKey] = struct{}{}
	}
	out := append([]Entry{}, existing...)
	for _, e := range incoming {
		if _, ok := seen[e.StableKey]; ok {
			continue
		}
		seen[e.StableKey] = struct{}{}
		out = append(out, e)
	}
	return out
}
