package serialize

import (
	"strings"
	"testing"
	"time"

	"subsync/internal/model"
)

func sampleDoc() *model.Document {
	return &model.Document{
		Format: model.FormatSRT,
		Cues: []model.Cue{
			{ID: "1", Track: "default", Order: 1, Start: time.Second, End: 2 * time.Second,
				Text: "line one\nline two"},
			{ID: "2", Track: "default", Order: 0, Start: 0, End: time.Second,
				Text: "earlier cue"},
		},
	}
}

func TestWriteSRTStableOrderAndEscaping(t *testing.T) {
	out, err := Write(sampleDoc(), model.FormatSRT)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "00:00:00,000 --> 00:00:01,000") {
		t.Errorf("ms timestamp/format wrong:\n%s", out)
	}
	// Write sorts by start time, so the originally later "earlier cue" wins.
	if strings.Index(out, "earlier cue") > strings.Index(out, "line one") {
		t.Error("cues not sorted by start time")
	}
	if !strings.Contains(out, "line one\nline two") {
		t.Error("multiline text not preserved")
	}
}

func TestWriteVTTHeaderAndEscape(t *testing.T) {
	doc := sampleDoc()
	out, err := Write(doc, model.FormatVTT)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "WEBVTT\n") {
		t.Error("VTT missing header")
	}
	if !strings.Contains(out, "00:00:01.000 -->") {
		t.Error("VTT must use dot fractional separator")
	}
	doc.Cues[0].Text = "a & b <c>"
	if out, _ = Write(doc, model.FormatVTT); !strings.Contains(out, "a &amp; b &lt;c&gt;") {
		t.Errorf("VTT escaping missing:\n%s", out)
	}
}

func TestWriteASSStructureAndPrecision(t *testing.T) {
	doc := &model.Document{
		Format: model.FormatASS,
		Styles: map[string]string{"Default": "Default,Arial,20,&H00FFFFFF"},
		Cues: []model.Cue{
			{ID: "ass-1", Track: "default", Order: 0, Start: 1500 * time.Millisecond,
				End: 2500 * time.Millisecond, Style: "Default", Text: "hard\nbreak"},
		},
	}
	out, err := Write(doc, model.FormatASS)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Script Info]", "[V4+ Styles]", "[Events]", "Dialogue:"} {
		if !strings.Contains(out, want) {
			t.Errorf("ASS output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "0:00:01.50,0:00:02.50,Default") {
		t.Errorf("ASS centisecond timestamp wrong:\n%s", out)
	}
	if !strings.Contains(out, "hard\\Nbreak") {
		t.Errorf("ASS newline escaping wrong:\n%s", out)
	}
}

func TestStyleLossAccounting(t *testing.T) {
	doc := &model.Document{Cues: []model.Cue{
		{Style: "Title", Text: "a"},
		{Style: "Default", Text: "b"},
		{Text: "c"},
	}}
	AccountStyleLoss(doc, model.FormatSRT)
	if doc.StyleLoss != 1 {
		t.Errorf("style loss = %d, want 1", doc.StyleLoss)
	}
	doc.StyleLoss = 0
	AccountStyleLoss(doc, model.FormatASS)
	if doc.StyleLoss != 0 {
		t.Error("ASS target must preserve styles without counting loss")
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, err := Write(sampleDoc(), model.Format("mkv")); err == nil {
		t.Error("expected error for unknown target format")
	}
}
