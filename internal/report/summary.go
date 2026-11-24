// Package report renders deterministic key=value acceptance summaries.
package report

import (
	"fmt"
	"io"
	"strings"
)

// Summary is the terminal acceptance report. Zero-value optional fields are
// still printed for the stable CLI contract.
type Summary struct {
	Mode                string `json:"mode"`
	Workdir             string `json:"workdir"`
	Seed                int64  `json:"seed"`
	FrameTotal          int    `json:"frame_total"`
	RecordTotal         int    `json:"record_total"`
	ValidFrames         int    `json:"valid_frames"`
	PresentFrames       int    `json:"present_frames"`
	MissingFrames       int    `json:"missing_frames"`
	BadSHA              int    `json:"bad_sha"`
	Truncated           int    `json:"truncated"`
	BadTimestamp        int    `json:"bad_timestamp"`
	FrameDuplicates     int    `json:"frame_duplicates"`
	AudioPackets        int    `json:"audio_packets"`
	StoredAudioRecords  int    `json:"stored_audio_records"`
	LatePackets         int    `json:"late_packets"`
	DuplicatePackets    int    `json:"duplicate_packets"`
	OutOfOrderPackets   int    `json:"out_of_order_packets"`
	LostPackets         int    `json:"lost_packets"`
	JitterEvents        int    `json:"jitter_events"`
	ConcealedPackets    int    `json:"concealed_packets"`
	ColorConflicts      int    `json:"color_conflicts"`
	SyncErrors          int    `json:"sync_errors"`
	UnknownChannels     int    `json:"unknown_channels"`
	LocationErrorMax    int64  `json:"location_error_max"`
	ChunkSize           int    `json:"chunk"`
	ChunkCount          int    `json:"chunk_count"`
	CommittedChunks     int    `json:"committed_chunks"`
	ResumedFromChunk    int    `json:"resumed_from_chunk"`
	HaveResume          bool   `json:"-"`
	RolledBackFromChunk int    `json:"rolled_back_from_chunk"`
	IndexCorrupt        bool   `json:"index_corrupt"`
	InterruptedAfter    int    `json:"interrupt_after,omitempty"`
	ResumeElapsedMS     int64  `json:"resume_elapsed_ms"`
	HaveResumeTiming    bool   `json:"-"`
	InputSHA256         string `json:"input_sha256"`
	OutputPath          string `json:"output_path"`
	OutputSHA256        string `json:"output_sha256"`
	MP4Tracks           int    `json:"mp4_tracks"`
	Completed           bool   `json:"completed"`
	ExitCode            int    `json:"exit_code"`
}

// Lines returns the stable terminal report lines.
func (s Summary) Lines() []string {
	var lines []string
	out := strings.Builder{}
	p := func(format string, args ...any) { fmt.Fprintf(&out, format+"\n", args...) }

	p("mode=%s", or(s.Mode, "demo"))
	if s.Workdir != "" {
		p("workdir=%s", s.Workdir)
	}
	p("seed=%d", s.Seed)
	p("frame_total=%d", s.FrameTotal)
	p("record_total=%d", s.RecordTotal)
	p("valid_frames=%d", s.ValidFrames)
	p("present_frames=%d", s.PresentFrames)
	p("missing_frames=%d", s.MissingFrames)
	p("bad_sha=%d", s.BadSHA)
	p("truncated=%d", s.Truncated)
	p("bad_timestamp=%d", s.BadTimestamp)
	p("duplicate_frames=%d", s.FrameDuplicates)
	p("audio_packets=%d", s.AudioPackets)
	p("stored_audio_records=%d", s.StoredAudioRecords)
	p("late_packets=%d", s.LatePackets)
	p("duplicate_packets=%d", s.DuplicatePackets)
	p("out_of_order_packets=%d", s.OutOfOrderPackets)
	p("lost_packets=%d", s.LostPackets)
	p("jitter_events=%d", s.JitterEvents)
	p("concealed_packets=%d", s.ConcealedPackets)
	p("color_conflicts=%d", s.ColorConflicts)
	p("sync_errors=%d", s.SyncErrors)
	p("unknown_channel_layouts=%d", s.UnknownChannels)
	p("location_error_max=%d", s.LocationErrorMax)
	p("定位误差=%d", s.LocationErrorMax)
	p("chunk=%d", s.ChunkSize)
	p("chunk_count=%d", s.ChunkCount)
	p("committed_chunks=%d", s.CommittedChunks)
	if s.HaveResume {
		p("resumed_from_chunk=%d", s.ResumedFromChunk)
	}
	if s.RolledBackFromChunk > 0 {
		p("rolled_back_from_chunk=%d", s.RolledBackFromChunk)
	}
	if s.IndexCorrupt {
		p("index_corrupt=%t", s.IndexCorrupt)
	}
	if s.HaveResumeTiming {
		p("resume_elapsed_ms=%d", s.ResumeElapsedMS)
		p("恢复耗时毫秒=%d", s.ResumeElapsedMS)
	}
	if s.InputSHA256 != "" {
		p("input_sha256=%s", s.InputSHA256)
	}
	if s.OutputPath != "" {
		p("output_path=%s", s.OutputPath)
	}
	if s.MP4Tracks > 0 {
		p("mp4_tracks=%d", s.MP4Tracks)
	}
	p("completed=%t", s.Completed)
	if s.OutputSHA256 != "" {
		p("output_sha256=%s", s.OutputSHA256)
		p("sha256=%s", s.OutputSHA256)
	}
	p("exit_code=%d", s.ExitCode)
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		lines = append(lines, line)
	}
	return lines
}

// Print writes all summary lines to w.
func (s Summary) Print(w io.Writer) {
	for _, line := range s.Lines() {
		fmt.Fprintln(w, line)
	}
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
