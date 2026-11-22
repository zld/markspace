// Package index parses delivered keyframe index files, validates every
// entry against the elementary stream it describes and reconciles the
// provable keyframe set with the bitstream scanner's findings.
//
// The on-disk format is line oriented and versioned:
//
//	# mediq keyframe index v1
//	# stream=<file> sha256=<hex>
//	<seq> <offset> <pts90k>
//
// Blank lines and comments are ignored. Deterministic ordering makes
// repeated runs byte-identical and keeps diffs reviewable.
package index

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediq/internal/timeline"
)

// Magic is the required first line of a keyframe index file.
const Magic = "# mediq keyframe index v1"

// Entry is one keyframe pointer.
type Entry struct {
	Seq    uint32
	Offset int64
	PTS    timeline.Ticks
}

// File is a parsed index before validation.
type File struct {
	Stream   string
	SHA256   string
	Entries  []Entry
	RawLines int // non-comment, non-blank lines accepted by the parser
}

// Status classifies an index entry after reconciliation.
type Status string

const (
	StatusValid     Status = "valid"      // points at a real keyframe
	StatusRecovered Status = "recovered"  // missing in index, proven from stream
	StatusDuplicate Status = "duplicate"  // same offset/seq as another entry
	StatusOutOfRange Status = "out_of_range"
	StatusMispoint  Status = "mispoint"   // parseable but no keyframe at offset
)

// Finding is one validated index row or one stream-proven keyframe.
type Finding struct {
	Entry  Entry
	Status Status
	Source string // "index" or "bitstream"
}

// Parse decodes an index file. Syntax problems are returned as
// timeline errors with offsets set to the 1-based input line; a
// malformed file still yields whatever entries parsed before the fault.
func Parse(r io.Reader) (*File, []*timeline.Error) {
	var errs []*timeline.Error
	f := &File{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	headerSeen := false
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#") {
			if !headerSeen {
				if text != Magic {
					errs = append(errs, timeline.New(timeline.StageIndex, timeline.KindInconsistent,
						int64(line), "reject_header", "missing or wrong index magic header"))
				}
				headerSeen = true
			}
			parseHeaderFields(f, text)
			continue
		}
		if !headerSeen {
			errs = append(errs, timeline.New(timeline.StageIndex, timeline.KindInconsistent,
				int64(line), "reject_entry", "entry before magic header"))
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			errs = append(errs, timeline.New(timeline.StageIndex, timeline.KindInconsistent,
				int64(line), "skip_entry",
				fmt.Sprintf("want 3 fields (seq offset pts), got %d", len(fields))))
			continue
		}
		e, perr := parseEntry(fields)
		if perr != nil {
			errs = append(errs, timeline.New(timeline.StageIndex, timeline.KindInconsistent,
				int64(line), "skip_entry", perr.Error()))
			continue
		}
		f.Entries = append(f.Entries, e)
		f.RawLines++
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, timeline.New(timeline.StageIndex, timeline.KindIO,
			int64(line), "abort_parse", err.Error()))
	}
	if !headerSeen {
		errs = append(errs, timeline.New(timeline.StageIndex, timeline.KindInconsistent,
			-1, "reject_header", "index contains no magic header"))
	}
	return f, errs
}

func parseHeaderFields(f *File, line string) {
	for _, field := range strings.Fields(line[1:]) {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "stream":
			f.Stream = kv[1]
		case "sha256":
			f.SHA256 = kv[1]
		}
	}
}

func parseEntry(fields []string) (Entry, error) {
	seq, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		return Entry{}, fmt.Errorf("bad seq %q: %w", fields[0], err)
	}
	off, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("bad offset %q: %w", fields[1], err)
	}
	if off < 0 {
		return Entry{}, fmt.Errorf("negative offset %d", off)
	}
	pts, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("bad pts %q: %w", fields[2], err)
	}
	return Entry{Seq: uint32(seq), Offset: off, PTS: timeline.Ticks(pts)}, nil
}

// Render serializes entries in the canonical index format.
func Render(stream, sha256Hex string, entries []Entry) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n# stream=%s sha256=%s\n", Magic, stream, sha256Hex)
	for _, e := range entries {
		fmt.Fprintf(&b, "%d %d %d\n", e.Seq, e.Offset, int64(e.PTS))
	}
	return b.Bytes()
}
