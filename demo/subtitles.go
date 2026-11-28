package demo

import (
	"context"
	"fmt"
	"os"

	"mediarc/model"
	"mediarc/sample"
	"mediarc/subtitle"
)

func (r *runner) runSubtitles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return model.New(model.CodeCanceled, "demo", "canceled before subtitles").WithCause(err)
	}
	clean := sample.BuildSRT(false)
	corrupt := sample.BuildSRT(true)
	vtt := sample.BuildVTT()
	for _, fixture := range []sample.SubtitleFixture{clean, corrupt, vtt} {
		if err := os.WriteFile(r.path(fixture.Name), fixture.Data, 0o644); err != nil {
			return model.Fatal(model.CodeInvalidArgument, "demo", "cannot write subtitle fixture").WithCause(err)
		}
		r.addFixture(fixture.Name)
	}
	cleanParsed := subtitle.ParseSRT(clean.Data)
	corruptParsed := subtitle.ParseSRT(corrupt.Data)
	vttParsed := subtitle.ParseWebVTT(vtt.Data)

	offsetsOK := true
	for _, issue := range corruptParsed.Issues {
		if issue.Offset < 0 || issue.Offset >= int64(len(corrupt.Data)) {
			offsetsOK = false
		}
	}
	preserved := false
	if len(cleanParsed.Cues) > 0 && len(cleanParsed.Cues[0].Lines) > 0 {
		line := cleanParsed.Cues[0].Lines[0]
		end := line.Offset + int64(len(line.Text))
		preserved = line.Number == 3 && end <= int64(len(clean.Data)) &&
			string(clean.Data[line.Offset:end]) == line.Text
	}
	checks := []Assertion{
		assert("clean SRT recovers every cue", fmt.Sprint(len(cleanParsed.Cues)), fmt.Sprint(clean.ValidCues),
			len(cleanParsed.Cues) == clean.ValidCues),
		assert("clean SRT reports no invalid cue", fmt.Sprint(len(cleanParsed.Issues)), "0", len(cleanParsed.Issues) == 0),
		assert("corrupt SRT still recovers the valid cues", fmt.Sprint(len(corruptParsed.Cues)), fmt.Sprint(corrupt.ValidCues),
			len(corruptParsed.Cues) == corrupt.ValidCues),
		assert("corrupt SRT reports each broken cue", fmt.Sprint(len(corruptParsed.Issues)), fmt.Sprint(corrupt.InvalidCues),
			len(corruptParsed.Issues) == corrupt.InvalidCues),
		assert("reported cue errors carry an in-file byte offset", boolText(offsetsOK), "true", offsetsOK),
		assert("cue text quotes back from its source byte offset", boolText(preserved), "true", preserved),
		assert("WebVTT recovers every cue", fmt.Sprint(len(vttParsed.Cues)), fmt.Sprint(vtt.ValidCues),
			len(vttParsed.Cues) == vtt.ValidCues),
		assert("WebVTT cue settings survive parsing", vttParsed.Cues[0].Settings, "align:start",
			len(vttParsed.Cues) > 0 && vttParsed.Cues[0].Settings == "align:start"),
	}
	r.metrics.CueChecksum = corruptParsed.Checksum
	r.record(Scenario{
		Name: "subtitles",
		Detail: fmt.Sprintf("%d clean SRT cues, %d recovered around %d bad blocks, %d VTT cues (checksum %016x)",
			len(cleanParsed.Cues), len(corruptParsed.Cues), len(corruptParsed.Issues), len(vttParsed.Cues), corruptParsed.Checksum),
		Checks: checks,
	})
	return nil
}

// ---------------------------------------------------------------- containers

func init() { register("subtitles", (*runner).runSubtitles) }
