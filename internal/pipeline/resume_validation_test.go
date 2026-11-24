package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/pipeline"
)

func TestResumeRejectsDigestAndParserMajorMismatchWithReasonFile(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		inputSHA    string
		parserMajor int
		wantCode    clock.ErrorCode
	}{
		{
			name: "digest", input: "same-bytes", inputSHA: "wrong-digest", parserMajor: pipeline.ParserMajor,
			wantCode: clock.CodeInputDigestMismatch,
		},
		{
			name: "parser-major", input: "parser-bytes", inputSHA: "", parserMajor: 2,
			wantCode: clock.CodeParserMajorMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, pipeline.SourceName)
			if err := os.WriteFile(src, []byte(tc.input), 0o644); err != nil {
				t.Fatal(err)
			}
			digest, err := pipeline.HashFile(src)
			if err != nil {
				t.Fatal(err)
			}
			inputSHA := tc.inputSHA
			if inputSHA == "" {
				inputSHA = digest
			}
			cp := clock.NewCheckpoint(inputSHA, tc.parserMajor, 100)
			cp.Completed = true
			cp.OutputSHA256 = "unused"
			if err := clock.SaveCheckpoint(filepath.Join(dir, pipeline.CheckpointName), cp); err != nil {
				t.Fatal(err)
			}

			_, _, err = pipeline.LoadResume(pipeline.Options{OutDir: dir})
			var rf *pipeline.ResumeFailure
			if !errors.As(err, &rf) {
				t.Fatalf("error=%T %v want ResumeFailure", err, err)
			}
			if rf.Reason.Code != tc.wantCode {
				t.Fatalf("code=%s want %s", rf.Reason.Code, tc.wantCode)
			}
			reasonBytes, err := os.ReadFile(filepath.Join(dir, pipeline.RejectionName))
			if err != nil {
				t.Fatalf("rejection reason was not preserved: %v", err)
			}
			if len(reasonBytes) == 0 || !strings.Contains(string(reasonBytes), string(tc.wantCode)) {
				t.Fatalf("reason file does not contain %s: %s", tc.wantCode, string(reasonBytes))
			}
		})
	}
}
