package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rtp-rebuild/internal/events"
	"rtp-rebuild/internal/fault"
)

// work holds all in-memory state for one stream. Every stage output is also
// persisted under stages/ so a resumed run can replay completed stages from
// disk instead of recomputing them.
type work struct {
	Name string
	// ExpectDamage marks the deliberately damaged self-test fixture. Its
	// reported damage is the expected outcome, not a tool failure.
	ExpectDamage bool
	Stream       streamData
	// Parsed events are not serialised; align needs them and re-reads the log
	// on resume.
	EventLog events.Log

	Reorder  reorderOut
	RTCP     rtcpOut
	Events   eventsOut
	Align    alignOut
	Mux      muxOut
	Validate validateOut

	parseErrors int
	// MuxMissing marks a stream for which no track could be rendered.
	MuxMissing bool
}

// streamData is the ingest output.
type streamData struct {
	// Path is deliberately excluded from the persisted form: the manifest must
	// not depend on where the workspace happens to live.
	Path            string `json:"-"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	StartUnixNano   int64  `json:"start_unix_nano"`
	Records         int    `json:"records"`
	RTPRecords      int    `json:"rtp_records"`
	RTCPRecords     int    `json:"rtcp_records"`
	EventRecords    int    `json:"event_records"`
	TruncatedOffset int64  `json:"truncated_offset"`
	// Truncated marks a stream that ended inside a record.
	Truncated bool `json:"truncated"`
}

type reorderOut struct {
	Packets        []packetJSON  `json:"packets"`
	Losses         []lossJSON    `json:"losses"`
	Anomalies      []anomalyJSON `json:"anomalies"`
	Duplicates     int           `json:"duplicates"`
	OutOfWindow    int           `json:"out_of_window"`
	TimestampWraps int           `json:"timestamp_wraps"`
	PeakPending    int           `json:"peak_pending_bytes"`
	LossTotal      int           `json:"loss_total"`
	LossRate       float64       `json:"loss_rate"`
	ParseErrors    int           `json:"parse_errors"`
	FirstError     string        `json:"first_error,omitempty"`
	FirstErrorOff  int64         `json:"first_error_offset"`
}

type packetJSON struct {
	Seq      uint16 `json:"seq"`
	ExtSeq   int64  `json:"ext_seq"`
	TS       int64  `json:"ts"`
	RawTS    uint32 `json:"raw_ts"`
	Marker   bool   `json:"marker"`
	Payload  []byte `json:"payload"`
	InputOff int64  `json:"input_off"`
	Suspect  bool   `json:"suspect"`
}

type lossJSON struct {
	First    uint16 `json:"first"`
	Last     uint16 `json:"last"`
	ExtFirst int64  `json:"ext_first"`
	Count    int    `json:"count"`
}

type anomalyJSON struct {
	Seq    uint16 `json:"seq"`
	ExtSeq int64  `json:"ext_seq"`
	Offset int64  `json:"offset"`
	Reason string `json:"reason"`
}

type rtcpOut struct {
	Reports     int     `json:"reports"`
	Blocks      int     `json:"blocks"`
	LostTotal   int64   `json:"lost_total"`
	Jitter      float64 `json:"jitter"`
	MaxJitter   uint32  `json:"max_jitter"`
	Errors      int     `json:"errors"`
	FirstError  string  `json:"first_error,omitempty"`
	TruncOffset int64   `json:"truncated_offset"`
	Complete    bool    `json:"complete"`
}

type eventsOut struct {
	Events      int `json:"events"`
	Unalignable int `json:"unalignable"`
}

type alignOut struct {
	Events      []alignEventJSON `json:"events"`
	Intervals   []intervalJSON   `json:"intervals"`
	Unalignable int              `json:"unalignable"`
	Threshold   int64            `json:"threshold_ticks"`
}

type alignEventJSON struct {
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Media     int64  `json:"media"`
	Interval  int    `json:"interval"`
	Alignable bool   `json:"alignable"`
	Reason    string `json:"reason,omitempty"`
}

type intervalJSON struct {
	StartSeq  uint16 `json:"start_seq"`
	EndSeq    uint16 `json:"end_seq"`
	StartTime int64  `json:"start_time"`
	EndTime   int64  `json:"end_time"`
	Gap       int64  `json:"gap"`
	Events    []int  `json:"events"`
}

type muxOut struct {
	Artifact    string `json:"artifact"`
	ArtifactSHA string `json:"artifact_sha256"`
	Samples     int    `json:"samples"`
	Duration    uint64 `json:"duration"`
	Bytes       int64  `json:"bytes"`
}

type validateOut struct {
	Items    []itemJSON `json:"items"`
	Passed   bool       `json:"passed"`
	Failures int        `json:"failures"`
}

type itemJSON struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
	// Expected marks a failed item on a fixture that is deliberately damaged.
	Expected bool `json:"expected,omitempty"`
}

// workspace is the output directory layout.
//
// Artifacts are written to a staging directory and only moved to Dir after
// validation passes, so a failed run never leaves a half-finished artifact in
// the published location.
type workspace struct {
	Dir       string
	StatePath string
	StageDir  string
	StageOut  string
}

func newWorkspace(dir string) (*workspace, error) {
	ws := &workspace{
		Dir:       dir,
		StatePath: filepath.Join(dir, "state.json"),
		StageDir:  filepath.Join(dir, "stages"),
		StageOut:  filepath.Join(dir, "staging"),
	}
	if err := os.MkdirAll(ws.StageDir, 0o755); err != nil {
		return nil, fault.New(fault.StageIngest, "create workspace", err)
	}
	if err := os.MkdirAll(ws.StageOut, 0o755); err != nil {
		return nil, fault.New(fault.StageIngest, "create staging", err)
	}
	return ws, nil
}

// artifactPath is the published location of a rendered track.
func (w *workspace) artifactPath(name string) string {
	return filepath.Join(w.Dir, name+".mp4")
}

// stagedPath is the unpublished location of a rendered track.
func (w *workspace) stagedPath(name string) string {
	return filepath.Join(w.StageOut, name+".mp4")
}

// publish moves a validated staged artifact into the published location.
func (w *workspace) publish(name string) error {
	if err := os.Rename(w.stagedPath(name), w.artifactPath(name)); err != nil {
		return fault.New(fault.StageMux, "publish "+name, err)
	}
	return nil
}

// discard removes staged artifacts left by a failed run.
func (w *workspace) discard(names []string) {
	for _, name := range names {
		os.Remove(w.stagedPath(name))
	}
}

func (w *workspace) manifestPath() string {
	return filepath.Join(w.Dir, "manifest.json")
}

func (w *workspace) stagePath(stageName, stream string) string {
	return filepath.Join(w.StageDir, fmt.Sprintf("%s.%s.json", stageName, stream))
}

// saveStage persists one stage output for one stream, atomically.
func (w *workspace) saveStage(stageName, stream string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fault.New(fault.StageManifest, "encode "+stageName, err)
	}
	if err := writeAtomic(w.stagePath(stageName, stream), data); err != nil {
		return fault.New(fault.StageManifest, "save "+stageName, err)
	}
	return nil
}

// loadStage reads a previously saved stage output.
func (w *workspace) loadStage(stageName, stream string, v any) error {
	data, err := os.ReadFile(w.stagePath(stageName, stream))
	if err != nil {
		return fault.New(fault.StageManifest, "load "+stageName, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fault.New(fault.StageManifest, "decode "+stageName, err)
	}
	return nil
}

// stageDigest hashes the persisted outputs of one stage across all streams.
func (w *workspace) stageDigest(stageName string, streams []*work) string {
	var digests []string
	for _, wk := range streams {
		data, err := os.ReadFile(w.stagePath(stageName, wk.Name))
		if err != nil {
			continue
		}
		digests = append(digests, digest(data))
	}
	return digestStrings("stage:"+stageName, digests...)
}

// writeAtomic writes data to a temporary file in the target directory and
// renames it over path, so a crash never leaves a partial file behind.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
