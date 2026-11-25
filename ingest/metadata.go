package ingest

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"mediapack/model"
	"mediapack/timeline"
)

// Metadata format used by the generated local fixtures:
//
//	TYPE<TAB>timebaseNum/timebaseDen<TAB>startTicks<TAB>...fields
//
// The supported TYPE values are SUB, CHAP, ID3, EMSG, and SEI. The format is
// intentionally small and explicit because only project-generated fixtures are
// in scope.
const (
	recordSubtitle = "SUB"
	recordChapter  = "CHAP"
	recordID3      = "ID3"
	recordEMSG     = "EMSG"
	recordSEI      = "SEI"
)

// MetadataResult contains accepted events and non-fatal bad-record warnings.
type MetadataResult struct {
	Events   []model.Event   `json:"events"`
	Warnings []model.Warning `json:"warnings"`
}

// ParseMetadata parses the supported line-oriented metadata fixture. Bad records
// are reported with line and byte offset and skipped; callers may continue.
func ParseMetadata(track model.TrackID, data []byte) (*MetadataResult, error) {
	res := &MetadataResult{}
	offset := 0
	for lineNo, line := range strings.Split(string(data), "\n") {
		lineStart := offset
		offset += len(line) + 1 // account for '\n'; final increment may pass EOF

		// Tolerate a trailing newline and CR.
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			warn := metadataError(track, lineNo+1, int64(lineStart), "record requires at least 4 tab fields", nil)
			res.Warnings = append(res.Warnings, toWarning(warn))
			continue
		}

		ev, err := parseRecord(track, lineNo+1, int64(lineStart), fields)
		if err != nil {
			if me, ok := model.AsError(err); ok {
				res.Warnings = append(res.Warnings, toWarning(me))
				continue
			}
			return nil, err
		}
		res.Events = append(res.Events, ev)
	}
	return res, nil
}

func parseRecord(track model.TrackID, lineNo int, offset int64, f []string) (model.Event, error) {
	kind := f[0]
	tb, err := parseTimebase(f[1])
	if err != nil {
		return model.Event{}, metadataError(track, lineNo, offset, "invalid rational timebase", err)
	}
	startTicks, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return model.Event{}, metadataError(track, lineNo, offset, "invalid start timestamp", err)
	}
	startNanos, err := tb.ToNanos(startTicks)
	if err != nil {
		return model.Event{}, metadataError(track, lineNo, offset, "cannot convert start timestamp", err)
	}
	source := fmt.Sprintf("line=%d", lineNo)
	base := model.Event{Track: track, Source: source, Offset: offset, StartNanos: startNanos}

	switch kind {
	case recordSubtitle, recordChapter:
		if len(f) < 5 {
			return model.Event{}, metadataError(track, lineNo, offset, kind+" requires end and text", nil)
		}
		endTicks, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return model.Event{}, metadataError(track, lineNo, offset, "invalid end timestamp", err)
		}
		endNanos, err := tb.ToNanos(endTicks)
		if err != nil {
			return model.Event{}, metadataError(track, lineNo, offset, "cannot convert end timestamp", err)
		}
		if endNanos < startNanos {
			return model.Event{}, metadataError(track, lineNo, offset, "event ends before it starts", nil)
		}
		base.Kind = model.KindSubtitle
		if kind == recordChapter {
			base.Kind = model.KindChapter
		}
		base.EndNanos = endNanos
		base.Text = f[4]
		base.Key = fmt.Sprintf("%s:%d", kind, lineNo)
		return base, nil

	case recordID3:
		if len(f) < 5 {
			return model.Event{}, metadataError(track, lineNo, offset, "ID3 requires pts, frame id, and value", nil)
		}
		base.Kind = model.KindID3
		base.Key = f[3]
		base.Value = strings.Join(f[4:], "\t")
		return base, nil

	case recordEMSG:
		if len(f) < 6 {
			return model.Event{}, metadataError(track, lineNo, offset, "EMSG requires duration, scheme, and message", nil)
		}
		durationTicks, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil || durationTicks < 0 {
			return model.Event{}, metadataError(track, lineNo, offset, "invalid emsg duration", err)
		}
		durationNanos, err := tb.ToNanos(durationTicks)
		if err != nil {
			return model.Event{}, metadataError(track, lineNo, offset, "cannot convert emsg duration", err)
		}
		base.Kind = model.KindEMSG
		base.EndNanos = startNanos + durationNanos
		base.Key = f[4] // scheme_id_uri
		base.Value = strings.Join(f[5:], "\t")
		return base, nil

	case recordSEI:
		if len(f) < 5 {
			return model.Event{}, metadataError(track, lineNo, offset, "SEI requires payload type and hex payload", nil)
		}
		payloadType, err := strconv.Atoi(f[3])
		if err != nil || payloadType < 0 || payloadType > 255 {
			return model.Event{}, metadataError(track, lineNo, offset, "invalid SEI payload type", err)
		}
		payload, err := hex.DecodeString(f[4])
		if err != nil {
			return model.Event{}, metadataError(track, lineNo, offset, "SEI payload is not hexadecimal", err)
		}
		base.Kind = model.KindSEI
		base.Key = fmt.Sprintf("sei-type-%d", payloadType)
		base.Value = hex.EncodeToString(payload)
		base.EndNanos = startNanos
		return base, nil

	default:
		return model.Event{}, metadataError(track, lineNo, offset, "unsupported metadata record type "+kind, nil)
	}
}

func parseTimebase(s string) (timeline.Timebase, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return timeline.Timebase{}, fmt.Errorf("timebase must be num/den")
	}
	num, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return timeline.Timebase{}, err
	}
	den, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return timeline.Timebase{}, err
	}
	tb := timeline.Timebase{Numerator: num, Denominator: den}
	return tb, tb.Validate()
}

func metadataError(track model.TrackID, line int, offset int64, msg string, cause error) *model.Error {
	return model.NewError(track, fmt.Sprintf("metadata:line=%d", line), offset,
		model.CategoryInvalidArgument, msg, cause).RecoverableWarn()
}

func toWarning(e *model.Error) model.Warning {
	return model.Warning{Track: e.Track, Offset: e.Offset, Category: e.Category, Message: e.Error()}
}
