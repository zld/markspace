package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mp4seek/internal/box"
	"mp4seek/internal/export"
	"mp4seek/internal/fixture"
	"mp4seek/internal/index"
	"mp4seek/internal/movie"
	"mp4seek/internal/seek"
)

// loaded bundles the parse products shared by inspect/locate/export.
type loaded struct {
	f   *os.File
	rd  *box.Reader
	m   *movie.Movie
	idx []*index.TrackIndex
}

func (l *loaded) close() { l.f.Close() }

// load opens path, parses moov and builds the index. Media payloads are
// never read here. The caller must close the result.
func load(path string) (*loaded, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	rd := box.NewReader(f, st.Size())
	m, err := movie.Parse(rd)
	if err != nil {
		f.Close()
		return nil, err
	}
	if m.Fragmented {
		f.Close()
		return nil, fmt.Errorf("fragmented MP4 (mvex/moof) is not supported")
	}
	idx, err := index.Build(m, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	return &loaded{f: f, rd: rd, m: m, idx: idx}, nil
}

// trackByID selects an index by track ID, falling back to 1-based order.
func trackByID(l *loaded, n int) (*index.TrackIndex, error) {
	for _, ti := range l.idx {
		if ti.Track.ID == n {
			return ti, nil
		}
	}
	if n >= 1 && n <= len(l.idx) {
		return l.idx[n-1], nil
	}
	return nil, fmt.Errorf("no track %d (file has %d tracks)", n, len(l.idx))
}

func cmdFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output directory for fixture files (required)")
	if fs.Parse(args) != nil || *out == "" {
		fs.Usage()
		return exitArgs
	}
	files, err := fixture.Generate(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		return exitCorrupt
	}
	for _, f := range files {
		fmt.Println(f)
	}
	return exitOK
}

func cmdInspect(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	in := fs.String("input", "", "input MP4 file (required)")
	if fs.Parse(args) != nil || *in == "" {
		fs.Usage()
		return exitArgs
	}
	l, err := load(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inspect:", err)
		return exitCorrupt
	}
	defer l.close()
	fmt.Printf("movie timescale: %d\n", l.m.Timescale)
	fmt.Printf("movie duration: %d (%.3f s)\n", l.m.Duration, float64(l.m.Duration)/float64(l.m.Timescale))
	fmt.Printf("tracks: %d\n", len(l.m.Tracks))
	for i, tr := range l.m.Tracks {
		ti := l.idx[i]
		fmt.Printf("track %d: handler=%s timescale=%d samples=%d sync=%d edits=%d wraps=%d tables={stts:%d ctts:%d stss:%d stsc:%d stsz:%d chunks:%d}\n",
			tr.ID, tr.Handler, tr.Timescale, len(ti.Samples), ti.SyncCount, len(tr.Edits), ti.Wraps,
			len(tr.Stts), len(tr.Ctts), len(tr.Stss), len(tr.Stsc), len(tr.Sizes), len(tr.Chunks))
	}
	diags, err := export.CrossCheck(*in, l.m, l.idx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inspect: cross-check:", err)
		return exitCorrupt
	}
	for _, d := range diags {
		fmt.Printf("diagnostic: %s\n", d)
	}
	if len(diags) == 0 {
		fmt.Println("diagnostics: none (go-mp4 cross-check agrees)")
	}
	return exitOK
}

// locateOutput is the locate/export result envelope.
type locateOutput struct {
	Target   string         `json:"target"`
	Track    int            `json:"track"`
	Status   string         `json:"status"`
	Record   *export.Record `json:"record,omitempty"`
}

func cmdLocate(args []string) int {
	fs := flag.NewFlagSet("locate", flag.ContinueOnError)
	in := fs.String("input", "", "input MP4 file (required)")
	track := fs.Int("track", 1, "track ID (or 1-based track number)")
	tm := fs.Float64("time", -1, "target movie time in seconds")
	smp := fs.Int("sample", -1, "target sample number (0-based)")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	if fs.Parse(args) != nil || *in == "" || (*tm < 0 && *smp < 0) {
		fs.Usage()
		return exitArgs
	}
	l, err := load(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate:", err)
		return exitCorrupt
	}
	defer l.close()
	ti, err := trackByID(l, *track)
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate:", err)
		return exitArgs
	}

	var res *seek.Result
	var target string
	if *smp >= 0 {
		res, err = seek.LocateSample(ti, *smp)
		target = fmt.Sprintf("sample=%d", *smp)
	} else {
		movieTime := int64(*tm * float64(l.m.Timescale))
		res, err = seek.LocateTime(ti, movieTime, l.m.Timescale)
		target = fmt.Sprintf("time=%.6f", *tm)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate:", err)
		if seek.IsNotLocatable(err) {
			return exitNotFound
		}
		return exitCorrupt
	}
	rec, err := export.NewRecord(l.rd, ti, res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate:", err)
		return exitCorrupt
	}
	out := locateOutput{Target: target, Track: ti.Track.ID, Status: "ok", Record: &rec}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return exitCorrupt
		}
	} else {
		fmt.Printf("target: %s\n", target)
		fmt.Printf("track: %d\n", ti.Track.ID)
		fmt.Printf("sample: %d\n", rec.Sample)
		fmt.Printf("pts: %.6f s\n", rec.PTSSeconds)
		fmt.Printf("dts: %d\n", rec.DTS)
		fmt.Printf("frame: %s\n", rec.FrameType)
		fmt.Printf("offset: %d\n", rec.Offset)
		fmt.Printf("size: %d\n", rec.Size)
		fmt.Printf("sha256: %s\n", rec.SHA256)
	}
	return exitOK
}

func cmdExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	in := fs.String("input", "", "input MP4 file (required)")
	track := fs.Int("track", 1, "track ID (or 1-based track number)")
	asJSON := fs.Bool("json", false, "emit JSON instead of line text")
	if fs.Parse(args) != nil || *in == "" {
		fs.Usage()
		return exitArgs
	}
	l, err := load(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		return exitCorrupt
	}
	defer l.close()
	ti, err := trackByID(l, *track)
	if err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		return exitArgs
	}
	var recs []export.Record
	for _, si := range ti.SyncSamples() {
		res, err := seek.LocateSample(ti, si)
		if err != nil {
			fmt.Fprintln(os.Stderr, "export:", err)
			return exitCorrupt
		}
		rec, err := export.NewRecord(l.rd, ti, res)
		if err != nil {
			fmt.Fprintln(os.Stderr, "export:", err)
			return exitCorrupt
		}
		recs = append(recs, rec)
	}
	if len(recs) == 0 {
		fmt.Fprintln(os.Stderr, "export: track has no sync samples")
		return exitNotFound
	}
	if *asJSON {
		if err := export.WriteJSON(os.Stdout, recs); err != nil {
			return exitCorrupt
		}
	} else {
		if err := export.WriteLines(os.Stdout, recs); err != nil {
			return exitCorrupt
		}
	}
	return exitOK
}
