package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeJob(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "job.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	p := writeJob(t, `{
		"renditions":[{"name":"720p","bitrate":1800000,"codec":"h264","first_seq":0,"last_seq":10}],
		"gap_timeout":"3s","drift_tolerance":150}`)
	job, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if job.GapTimeout.Duration != 3*time.Second {
		t.Fatalf("gap timeout = %v", job.GapTimeout)
	}
	if job.DriftTolerance.Duration != 150*time.Millisecond {
		t.Fatalf("tolerance = %v", job.DriftTolerance)
	}
	if got := job.Renditions[0].ExpectedCount(); got != 11 {
		t.Fatalf("expected count = %d", got)
	}
}

func TestValidationFailures(t *testing.T) {
	cases := map[string]string{
		"empty":            `{"renditions":[]}`,
		"duplicate name":   `{"renditions":[{"name":"a","bitrate":1,"codec":"h264","first_seq":0,"last_seq":1},{"name":"a","bitrate":2,"codec":"h264","first_seq":0,"last_seq":1}]}`,
		"bad codec":        `{"renditions":[{"name":"a","bitrate":1,"codec":"hevc","first_seq":0,"last_seq":1}]}`,
		"bad bitrate":      `{"renditions":[{"name":"a","bitrate":0,"codec":"h264","first_seq":0,"last_seq":1}]}`,
		"bad range":        `{"renditions":[{"name":"a","bitrate":1,"codec":"h264","first_seq":5,"last_seq":2}]}`,
		"path traversal":   `{"renditions":[{"name":"../a","bitrate":1,"codec":"h264","first_seq":0,"last_seq":1}]}`,
		"duplicate bitrate": `{"renditions":[{"name":"a","bitrate":9,"codec":"h264","first_seq":0,"last_seq":1},{"name":"b","bitrate":9,"codec":"h264","first_seq":0,"last_seq":1}]}`,
		"unknown fault":    `{"renditions":[{"name":"a","bitrate":1,"codec":"h264","first_seq":0,"last_seq":1}],"faults":[{"rendition":"x","segment":1,"offset":0,"xor":1}]}`,
		"bad duration":     `{"renditions":[{"name":"a","bitrate":1,"codec":"h264","first_seq":0,"last_seq":1}],"gap_timeout":"nope"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeJob(t, body)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing job file")
	}
}
