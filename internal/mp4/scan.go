package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	gomp4 "github.com/abema/go-mp4"

	"mediapipeline/internal/evidence"
	"mediapipeline/pkg/media"
)

// BoxLocation is a compact record of a box encountered during structure scan.
type BoxLocation struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Offset     int64  `json:"offset"`
	Size       int64  `json:"size"`
	HeaderSize int64  `json:"header_size"`
	TrackID    uint32 `json:"track_id,omitempty"`
	Supported  bool   `json:"supported"`
}

// StructureError annotates a go-mp4 traversal failure with its box location.
type StructureError struct {
	Location BoxLocation
	Err      error
}

func (e *StructureError) Error() string {
	return fmt.Sprintf("mp4 structure error at %s offset=%d: %v", e.Location.Path, e.Location.Offset, e.Err)
}
func (e *StructureError) Unwrap() error { return e.Err }

// ScanFile hashes and parses one local MP4/MOV file. Corrupt container inputs
// are returned in info.Report, not as a Go error; missing/unreadable files are.
func ScanFile(path string, chunkBytes int) (*media.ContainerInfo, error) {
	sum, size, err := evidence.SHA256File(path)
	if err != nil {
		return nil, err
	}
	info := &media.ContainerInfo{Path: path, FileSize: size, SourceSHA256: sum}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	locs, structErr := InspectStructure(f)
	for _, loc := range locs {
		if !loc.Supported && loc.Type != "free" && loc.Type != "skip" && loc.Type != "wide" {
			info.Report.Warning(media.ClassUnsupportedBox, "unknown or unsupported box retained by location",
				media.WithBox(loc.Type, loc.Offset), media.WithScope(media.ScopeContainer))
		}
	}

	probe, probeErr := gomp4.Probe(f)
	if probeErr != nil {
		ids := trackIDsFromLocations(locs)
		if len(ids) == 0 {
			ids = bestEffortTrackIDs(info.Path)
		}
		addProbeFault(info, locs, ids, structErr, probeErr)
		return info, nil
	}

	info.MajorBrand = brandString(probe.MajorBrand)
	info.MinorVersion = probe.MinorVersion
	info.FastStart = probe.FastStart
	info.MovieTimebase = media.Timebase{Timescale: probe.Timescale}
	info.MovieDuration = probe.Duration

	keyframeSets := extractKeyframeSets(f)
	for i, pt := range probe.Tracks {
		tid := pt.TrackID
		track := media.TrackInfo{
			TrackID:       tid,
			Handler:       codecName(pt.Codec),
			Timebase:      media.Timebase{Timescale: pt.Timescale},
			MovieTimebase: info.MovieTimebase,
			DurationTicks: pt.Duration,
			MediaDuration: pt.Duration,
			EditList:      convertEdits(pt.EditList),
			SampleCount:   uint32(len(pt.Samples)),
			Keyframes:     keyframeSetForTrack(keyframeSets, i, tid),
		}
		if track.StartTimeTicks = firstEditMediaTime(pt.EditList); track.StartTimeTicks < 0 {
			// Empty edit is represented by -1 in ISO boxes; the media clock starts at 0.
			track.StartTimeTicks = 0
		}
		attachSampleDescription(f, locs, tid, &track)
		if err := populateSamples(f, pt, &track, locs, size, chunkBytes, &info.Report); err != nil {
			return nil, err
		}
		track.TrackSHA256 = hashTrack(&track)
		info.Tracks = append(info.Tracks, track)
	}

	// A truncated moov can occasionally be accepted by Probe if truncation is in
	// trailing optional boxes. Structure traversal remains authoritative.
	if structErr != nil && isTruncationError(structErr) {
		loc := lastMoovLocation(locs)
		info.Report.Error(media.ClassTruncatedMoov, "moov/box hierarchy is truncated",
			media.WithBox(loc.Type, loc.Offset), media.WithTracks(trackIDsFromLocations(locs)...),
			media.WithScope(media.ScopeTrack), media.WithCause(structErr))
	}
	return info, nil
}

// InspectStructure walks the complete box hierarchy with go-mp4.
func InspectStructure(r io.ReadSeeker) ([]BoxLocation, error) {
	var locs []BoxLocation
	var structureErr error
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (any, error) {
		t := h.BoxInfo.Type.String()
		loc := BoxLocation{
			Path:       pathString(h.Path),
			Type:       t,
			Offset:     int64(h.BoxInfo.Offset),
			Size:       int64(h.BoxInfo.Size),
			HeaderSize: int64(h.BoxInfo.HeaderSize),
			Supported:  h.BoxInfo.IsSupportedType(),
		}
		if t == "tkhd" {
			if box, _, perr := h.ReadPayload(); perr == nil {
				if tkhd, ok := box.(*gomp4.Tkhd); ok {
					loc.TrackID = tkhd.TrackID
				}
			}
		}
		locs = append(locs, loc)
		if _, ierr := h.Expand(); ierr != nil {
			structureErr = &StructureError{Location: loc, Err: ierr}
			return nil, ierr
		}
		return nil, nil
	})
	if err != nil && structureErr == nil {
		structureErr = err
	}
	return locs, structureErr
}

func addProbeFault(info *media.ContainerInfo, locs []BoxLocation, trackIDs []uint32, structErr error, probeErr error) {
	msg := probeErr.Error()
	loc := lastMoovLocation(locs)
	offset := loc.Offset
	box := loc.Type
	if box == "" {
		box = "moov"
	}
	if isTruncationError(probeErr) || isTruncationError(structErr) || strings.Contains(msg, "too large box size") {
		if l, ok := lastLocation(locs, ""); ok {
			loc = l
			offset = l.Offset
			box = l.Type
		}
		info.Report.Error(media.ClassTruncatedMoov, "container metadata is truncated or exceeds file boundary",
			media.WithBox(box, offset), media.WithTracks(trackIDs...),
			media.WithScope(media.ScopeTrack), media.WithCause(errors.Join(probeErr, structErr)))
		return
	}
	info.Report.Error("mp4_probe_error", msg,
		media.WithBox(box, offset), media.WithTracks(trackIDs...),
		media.WithScope(media.ScopeAll), media.WithCause(probeErr))
}

func isTruncationError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unexpected eof") ||
		strings.Contains(s, "box size exceeds file size") ||
		strings.Contains(s, "too large box size") ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF)
}

func pathString(p gomp4.BoxPath) string {
	parts := make([]string, 0, len(p))
	for _, t := range p {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, "/")
}

func brandString(b [4]byte) string {
	return strings.TrimRight(string(b[:]), " \x00")
}

func codecName(c gomp4.Codec) string {
	switch c {
	case gomp4.CodecAVC1:
		return "avc1"
	case gomp4.CodecMP4A:
		return "mp4a"
	default:
		return "unknown"
	}
}

func convertEdits(in gomp4.EditList) []media.EditListEntry {
	out := make([]media.EditListEntry, 0, len(in))
	for _, e := range in {
		if e == nil {
			continue
		}
		out = append(out, media.EditListEntry{
			SegmentDuration: e.SegmentDuration,
			MediaTime:       e.MediaTime,
			MediaRate:       1,
		})
	}
	return out
}

func firstEditMediaTime(edits gomp4.EditList) int64 {
	if len(edits) == 0 || edits[0] == nil {
		return 0
	}
	return edits[0].MediaTime
}

func lastMoovLocation(locs []BoxLocation) BoxLocation {
	for i := len(locs) - 1; i >= 0; i-- {
		if strings.Contains(locs[i].Path, "moov") {
			return locs[i]
		}
	}
	return BoxLocation{Type: "moov", Offset: -1}
}

func lastLocation(locs []BoxLocation, typ string) (BoxLocation, bool) {
	for i := len(locs) - 1; i >= 0; i-- {
		if typ == "" || locs[i].Type == typ {
			return locs[i], true
		}
	}
	return BoxLocation{}, false
}

func trackIDsFromLocations(locs []BoxLocation) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, l := range locs {
		if l.TrackID != 0 && !seen[l.TrackID] {
			seen[l.TrackID] = true
			out = append(out, l.TrackID)
		}
	}
	return out
}

// bestEffortTrackIDs finds intact tkhd boxes even when an outer truncated moov
// prevents a full hierarchy expansion.
func bestEffortTrackIDs(path string) []uint32 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	needle := []byte("tkhd")
	seen := map[uint32]bool{}
	var out []uint32
	start := 0
	for {
		idx := bytes.Index(b[start:], needle)
		if idx < 0 {
			break
		}
		pos := start + idx
		start = pos + 4
		// "tkhd" at pos; track_ID is 16 bytes after the four-byte type.
		if pos+20 > len(b) || pos < 4 {
			continue
		}
		id := binary.BigEndian.Uint32(b[pos+16 : pos+20])
		if id != 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
