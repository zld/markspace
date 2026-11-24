package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Session lists what Generate wrote.
type Session struct {
	Root     string
	Manifest *Manifest
	Tables   map[string]*Table
	RTPFiles map[string]string
}

func Generate(dir string, seed int64, targetBps int64, corrupt string) (*Session, error) {
	mediaDir := filepath.Join(dir, "media")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return nil, err
	}
	sess := &Session{Root: dir, Tables: map[string]*Table{}, RTPFiles: map[string]string{}}
	sessionNs := 1_700_000_000_000_000_000 + seed*SecondNs
	m := &Manifest{Version: 1, Seed: seed, Corruption: corrupt, SessionStartNs: sessionNs, TargetBps: targetBps}

	cams := []struct {
		name  string
		seed  int64
		start int64
		fps   float64
		step  int
	}{
		{"cam-a", seed + 11, 0, 30, 30},
		{"cam-b", seed + 23, 250_000_000, 25, 25},
	}
	// Each camera carries an equal share of the delivery target so the
	// aggregate measured bitrate matches the requested target.
	perCamBps := targetBps / int64(len(cams))
	for ci, c := range cams {
		step := c.step
		missing := false
		overrun := int64(0)
		stuck := 0
		switch {
		case corrupt == CorruptSparseKey && ci == 0:
			step = 330 // > 10 s at 30 fps
		case corrupt == CorruptMissingSync && ci == 0:
			missing = true
		case corrupt == CorruptEditOverrun && ci == 0:
			overrun = 30_000
		case corrupt == CorruptDTSRegression && ci == 0:
			stuck = 3
		}
		spec := Spec{
			Camera: c.name, FileName: c.name + ".mp4",
			Timescale: 90000, FPS: c.fps, RecordingSec: 12,
			KeyframeStep: step, WindowBytes: perCamBps / 8 * 8,
			EditStartSec: 2, EditEndSec: 10,
			BytesPerSec: int(perCamBps / 8),
			Width:       1920, Height: 1080, Seed: c.seed,
			MissingSync: missing, EditOverrunMs: overrun, StuckTail: stuck,
		}
		// Press-start offset is one frame period for the second camera, so the
		// edit-list trim lands exactly on its frame grid while still exercising
		// a nonzero stored origin.
		spec.PressStartOffsetT = int64(ci) * int64(spec.Delta())
		t, err := writeTrack(filepath.Join(mediaDir, spec.FileName), spec)
		if err != nil {
			return nil, err
		}
		sess.Tables[c.name] = t

		if corrupt == CorruptTruncatedTable && ci == 0 {
			if err := truncateSampleTable(filepath.Join(mediaDir, spec.FileName)); err != nil {
				return nil, err
			}
		}

		rtpPath := filepath.Join(mediaDir, c.name+".rtp.jsonl")
		wrap := corrupt == CorruptRTPWrap
		if err := writeRTP(rtpPath, t, c.start, wrap); err != nil {
			return nil, err
		}
		sess.RTPFiles[c.name] = rtpPath

		// Editorial boundaries are session-relative offsets from the session
		// start, rounded to the camera's frame grid so expected in/out points
		// are reachable exactly.
		editStart := gridAlign(c.start+int64(2*SecondNs), c.start, c.fps)
		editEnd := gridAlign(c.start+int64(10*SecondNs), c.start, c.fps)
		m.Entries = append(m.Entries, Entry{
			Camera: c.name,
			File:   "media/" + spec.FileName,
			Tracks: []TrackSpec{{TrackID: 1, Kind: "video", Timescale: spec.Timescale,
				// start_ns is the pre-roll trimmed by the edit list, i.e. the
				// stored origin relative to the recording start.
				StartNs: spec.PressStartOffsetT * SecondNs / int64(spec.Timescale)}},
			Recording: Interval{StartNs: c.start, EndNs: c.start + int64(12*SecondNs)},
			Editorial: Interval{StartNs: editStart, EndNs: editEnd},
		})
	}
	sess.Manifest = m
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(filepath.Join(dir, "manifest.json"), append(raw, '\n')); err != nil {
		return nil, err
	}
	return sess, nil
}

// writeTrack lays out and writes one MP4. Sample offsets are resolved by
// iterating because mdat placement depends on moov size; moov size does not
// depend on offset values, so this converges in two passes.
func writeTrack(path string, spec Spec) (*Table, error) {
	samples := buildSamples(spec, 0)
	var t *Table
	for pass := 0; pass < 3; pass++ {
		probe := &Table{Spec: spec, Samples: samples, MovieTimescale: 1000}
		probe.EditMediaTime = spec.PressStartOffsetT
		probe.MediaTicks = int64(len(samples)) * int64(spec.Delta())
		// Presented duration covers the last sample (its PTS, not merely the
		// DTS end), so edit-list coverage can be validated against repair.
		lastPTS := probe.MediaTicks
		for _, s := range samples {
			if s.PTS > lastPTS {
				lastPTS = s.PTS
			}
		}
		presentedTicks := lastPTS - spec.PressStartOffsetT
		probe.EditMovieDur = uint64(presentedTicks) * 1000 / uint64(spec.Timescale)
		if spec.EditOverrunMs > 0 {
			probe.EditMovieDur += uint64(spec.EditOverrunMs)
		}
		if err := writeFile(path, probe); err != nil {
			return nil, err
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		probe.FileSize = fi.Size()
		t = probe
		changed := false
		for i := range samples {
			want := t.MdatOffset
			if i > 0 {
				want = samples[i-1].Offset + uint64(samples[i-1].Size)
			}
			if samples[i].Offset != want {
				samples[i].Offset = want
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return t, nil
}
