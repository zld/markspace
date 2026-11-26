package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"replayctl/internal/event"
	mp4scan "replayctl/internal/mp4"
	"replayctl/internal/timeline"
)

const (
	CaseOK        = "ok"
	CaseTruncated = "truncated-box"
	CaseTimeGap   = "time-gap"
	CaseWrap      = "timestamp-wrap"
	CaseRecover   = "recover"
)

// Options controls self-fixture generation.
type Options struct {
	Root    string
	Case    string
	EventMS int64
}

// Paths points at generated fixture inputs and its edit decision list.
type Paths struct {
	Dir       string
	MediaPath string
	EventPath string
	EventID   string
	Case      string
	Timeline  timeline.Timeline
}

// Root returns the default persistent self-fixture root.
func Root() string {
	return filepath.Join(os.TempDir(), "replayctl-self-fixtures")
}

// GenerateSelf creates deterministic local inputs for an acceptance case.
func GenerateSelf(opt Options) (*Paths, error) {
	if opt.Case == "" {
		opt.Case = CaseOK
	}
	if opt.EventMS == 0 {
		opt.EventMS = EventMS
	}
	root := opt.Root
	if root == "" {
		root = Root()
	}
	dir := filepath.Join(root, opt.Case)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := &Paths{Dir: dir, Case: opt.Case, EventID: fmt.Sprintf("kill-%dms", opt.EventMS)}

	if opt.Case == CaseTruncated {
		p.MediaPath = filepath.Join(dir, "truncated-box.mp4")
		if err := writeTruncatedBox(p.MediaPath); err != nil {
			return nil, err
		}
		return p, nil
	}

	media, err := GenerateMedia()
	if err != nil {
		return nil, err
	}
	p.MediaPath = filepath.Join(dir, "source.mp4")
	if err := mp4scan.MuxFile(p.MediaPath, []mp4scan.TrackIn{media.Video, media.Audio}); err != nil {
		return nil, err
	}
	p.EventPath = filepath.Join(dir, "events.rtp")
	if err := writeEvents(p.EventPath, opt.EventMS, p.EventID); err != nil {
		return nil, err
	}
	if opt.Case == CaseTimeGap {
		p.Timeline = gapTimeline(p.MediaPath)
	} else {
		p.Timeline = StandardTimeline(p.MediaPath)
	}
	return p, nil
}

func writeTruncatedBox(path string) error {
	var buf [16]byte
	binary.BigEndian.PutUint32(buf[0:4], 64) // declares a 64-byte box...
	copy(buf[4:8], "ftyp")
	// ...but the complete file is exactly 16 bytes. Bytes 8..15 are payload.
	return os.WriteFile(path, buf[:], 0o600)
}

func writeEvents(path string, eventMS int64, eventID string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	base := event.NearWrapBase()
	origin, err := event.MarshalPacket(0, base, "clock-origin", "origin")
	if err != nil {
		return err
	}
	if _, err := event.WriteRecord(f, origin); err != nil {
		return err
	}
	targetTS := event.WrappedTimestampAt(base, eventMS*int64(event.ClockRate/1000))
	target, err := event.MarshalPacket(1, targetTS, EventKind, eventID)
	if err != nil {
		return err
	}
	if _, err := event.WriteRecord(f, target); err != nil {
		return err
	}
	return f.Sync()
}

// StandardTimeline returns two overlapping clips with a 200 ms linear transition.
func StandardTimeline(mediaPath string) timeline.Timeline {
	return timeline.Timeline{
		Clips: []timeline.Clip{
			{ID: "cam-a", Track: "V1", SourcePath: mediaPath, StartMS: 0, EndMS: 1100, SourceInMS: 0},
			{ID: "cam-b", Track: "V2", SourcePath: mediaPath, StartMS: 900, EndMS: 2000, SourceInMS: 900},
		},
		Transitions: []timeline.Transition{
			{ID: "dissolve-1", FromID: "cam-a", ToID: "cam-b", StartMS: 900, EndMS: 1100},
		},
	}
}

func gapTimeline(mediaPath string) timeline.Timeline {
	return timeline.Timeline{
		Clips: []timeline.Clip{
			{ID: "before-gap", Track: "V1", SourcePath: mediaPath, StartMS: 0, EndMS: 800, SourceInMS: 0},
			{ID: "after-gap", Track: "V1", SourcePath: mediaPath, StartMS: 1200, EndMS: 2000, SourceInMS: 1200},
		},
	}
}
