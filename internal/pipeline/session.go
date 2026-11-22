package pipeline

import (
	"encoding/binary"
	"errors"

	"github.com/pairwise/mediad/internal/audio"
	"github.com/pairwise/mediad/internal/clock"
	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/index"
	"github.com/pairwise/mediad/internal/mp4"
)

// Session is a fully wired analysis of one file: parsed movie, per-track
// clocks, a keyframe index, and silence results.
type Session struct {
	Path     string
	Tree     *mp4.Tree
	Movie    *mp4.Movie
	Clocks   map[uint32]*clock.TrackClock
	Pres     *Presentation
	Index    *index.Index
	Silence  map[uint32]audio.Result
	Warnings []*fault.Error
	Err      error // recoverable damage discovered while opening
}

// OpenSession parses a file and wires every downstream stage. A recoverable
// error is returned in Session.Err so callers can still use the partial
// analysis.
func OpenSession(path string) (*Session, error) {
	tree, err := mp4.Open(path)
	if err != nil {
		return nil, err
	}
	movie, perr := mp4.ParseMovie(tree)
	if len(movie.Tracks) == 0 {
		tree.Close()
		if perr != nil {
			return nil, perr
		}
		return nil, fault.Errorf("pipeline.session", fault.KindParse, "no tracks").WithPath(path).Fatal()
	}
	s := &Session{Path: path, Tree: tree, Movie: movie, Warnings: movie.Warnings, Err: perr}

	s.Clocks = map[uint32]*clock.TrackClock{}
	var problems []error
	for _, tr := range movie.Tracks {
		edits, err := convertEdits(tr, movie.Timescale)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		offset, err := trackOffset(movie, tr)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		clk, err := clock.NewTrackClock(path, tr.ID, tr.Timescale, clock.NanosecondTimeline(), edits, offset)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		s.Clocks[tr.ID] = clk
	}
	s.Pres = ComputePresentation(movie, s.Clocks)
	for _, tr := range movie.Tracks {
		if clk := s.Clocks[tr.ID]; clk != nil && clk.WrapDetected() {
			problems = append(problems, fault.Errorf("pipeline.session", fault.KindTimeline,
				"track %d clock wrapped or regressed; samples after the fault are withheld", tr.ID).
				WithPath(path).WithTrack(tr.ID).Recovered().Wrapped(fault.ErrTimeWrap))
		}
	}
	ix, ierr := index.Build(movie, s.Pres)
	if ierr != nil {
		problems = append(problems, ierr)
	}
	s.Index = ix
	s.Silence = analyzeAudio(tree, movie, s.Pres)
	for _, tr := range movie.Tracks {
		if res, ok := s.Silence[tr.ID]; ok && !res.Detectable && tr.IsAudio() {
			s.Warnings = append(s.Warnings, fault.Errorf("pipeline.audio", fault.KindAudio,
				"track %d silence undetectable: %s", tr.ID, res.Reason).
				WithPath(path).WithTrack(tr.ID).Recovered())
		}
	}
	if len(problems) > 0 {
		joined := errors.Join(problems...)
		if s.Err == nil {
			s.Err = joined
		}
	}
	return s, nil
}

// Close releases the underlying file.
func (s *Session) Close() error {
	if s.Tree != nil {
		return s.Tree.Close()
	}
	return nil
}

// Planner builds the segmentation planner for this session.
func (s *Session) Planner() (*Planner, error) {
	return NewPlanner(s.Movie, s.Tree, s.Index, s.Pres, s.Silence)
}

// Requests returns one full-range request per video track. When no video
// track survives, audio tracks are used so damaged files still yield slices.
func (s *Session) Requests() []Request {
	var out []Request
	for _, ti := range s.Index.Tracks {
		end := ti.Duration
		if end <= 0 {
			continue
		}
		out = append(out, Request{TrackID: ti.TrackID, StartNS: 0, EndNS: end + 1})
	}
	if len(out) == 0 {
		for _, tr := range s.Movie.Tracks {
			n := len(tr.Samples)
			if n == 0 {
				continue
			}
			end, _ := s.Pres.At(tr.ID, n-1)
			if end > 0 {
				out = append(out, Request{TrackID: tr.ID, StartNS: 0, EndNS: end + 1})
			}
		}
	}
	return out
}

func convertEdits(tr *mp4.Track, movieScale uint32) ([]clock.Edit, error) {
	if movieScale == 0 {
		return nil, fault.Errorf("pipeline.edits", fault.KindTimeline, "zero movie timescale").WithTrack(tr.ID)
	}
	tl := clock.NanosecondTimeline()
	out := make([]clock.Edit, 0, len(tr.Edits))
	for _, e := range tr.Edits {
		seg, err := tl.Convert(e.SegmentTicks, movieScale)
		if err != nil {
			return nil, fault.New("pipeline.edits", fault.KindTimeline, err).WithTrack(tr.ID)
		}
		media := e.MediaTicks
		if media >= 0 {
			media, err = tl.Convert(uint64(e.MediaTicks), tr.Timescale)
			if err != nil {
				return nil, fault.New("pipeline.edits", fault.KindTimeline, err).WithTrack(tr.ID)
			}
		}
		out = append(out, clock.Edit{SegmentDuration: uint64(seg), MediaTime: media, RateFixed: e.RateFixed})
	}
	return out, nil
}

// trackOffset anchors a track's first presented sample at zero. Tracks with
// an edit list already start where the editor wanted them to.
func trackOffset(movie *mp4.Movie, tr *mp4.Track) (int64, error) {
	if len(tr.Edits) > 0 || len(tr.Samples) == 0 {
		return 0, nil
	}
	tl := clock.NanosecondTimeline()
	first, err := tl.Convert(tr.Samples[0].DTS, tr.Timescale)
	if err != nil {
		return 0, fault.New("pipeline.offset", fault.KindTimeline, err).WithTrack(tr.ID)
	}
	if first < 0 {
		return 0, fault.Errorf("pipeline.offset", fault.KindTimeline, "negative first timestamp").WithTrack(tr.ID)
	}
	return first, nil
}

func analyzeAudio(tree *mp4.Tree, movie *mp4.Movie, pres *Presentation) map[uint32]audio.Result {
	out := map[uint32]audio.Result{}
	cfg := audio.DefaultConfig()
	for _, tr := range movie.Tracks {
		if !tr.IsAudio() {
			continue
		}
		switch tr.Entry {
		case "twos", "sowt", "lpcm", "raw ", "in24", "in32":
			res, err := pcmResult(tree, tr, pres, cfg)
			if err != nil {
				res = audio.Undetectable(tr.ID, err.Error())
			}
			out[tr.ID] = res
		default:
			out[tr.ID] = audio.Undetectable(tr.ID, "compressed audio ("+tr.Entry+") is not decoded")
		}
	}
	return out
}

func pcmResult(tree *mp4.Tree, tr *mp4.Track, pres *Presentation, cfg audio.Config) (audio.Result, error) {
	if tr.SampleRate == 0 || tr.Channels == 0 {
		return audio.Undetectable(tr.ID, "missing sample rate or channel count"), nil
	}
	start := int64(0)
	if v, ok := pres.At(tr.ID, 0); ok {
		start = v
	}
	var pcm []int16
	for _, s := range tr.Samples {
		if !s.InMdat || s.Size < 2 {
			continue
		}
		buf, err := tree.ReadAt(s.Offset, int64(s.Size))
		if err != nil {
			return audio.Result{}, err
		}
		le := tr.Entry == "sowt" || tr.Entry == "lpcm" || tr.Entry == "twos"
		for i := 0; i+1 < len(buf); i += 2 {
			if le {
				pcm = append(pcm, int16(binary.LittleEndian.Uint16(buf[i:])))
			} else {
				pcm = append(pcm, int16(binary.BigEndian.Uint16(buf[i:])))
			}
		}
	}
	if len(pcm) == 0 {
		return audio.Undetectable(tr.ID, "no readable PCM payload"), nil
	}
	return audio.AnalyzePCM(tr.ID, pcm, tr.Channels, int(tr.SampleRate), start, cfg)
}
