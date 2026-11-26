package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"assetcheck/internal/media"
)

const SchemaVersion = 1

// StableKey contains exactly the identity inputs allowed to define a task or
// item. Delivery sequence/count are deliberately absent.
func StableKey(assetID, language, trackType, contentDigest string) string {
	h := sha256.New()
	fmt.Fprintf(h, "asset_id=%s\n", assetID)
	fmt.Fprintf(h, "language=%s\n", language)
	fmt.Fprintf(h, "track_type=%s\n", trackType)
	fmt.Fprintf(h, "content_sha256=%s\n", contentDigest)
	return hex.EncodeToString(h.Sum(nil))
}

type Metrics struct {
	TargetBPS  int64   `json:"target_bps"`
	ActualBPS  int64   `json:"actual_bps"`
	DeltaPct   float64 `json:"delta_pct"`
	PSNRDB     float64 `json:"psnr_db"`
	AlignMaxMS float64 `json:"align_max_ms"`
}

type Item struct {
	StableKey  string  `json:"stable_key"`
	AssetID    string  `json:"asset_id"`
	Language   string  `json:"language"`
	TrackType  string  `json:"track_type"`
	TrackID    int     `json:"track_id"`
	Blocks     int     `json:"blocks"`
	AlignMaxMS float64 `json:"align_max_ms"`
	TargetBPS  int64   `json:"target_bps,omitempty"`
	ActualBPS  float64 `json:"actual_bps,omitempty"`
	DeltaPct   float64 `json:"delta_pct,omitempty"`
	PSNRDB     float64 `json:"psnr_db,omitempty"`
}

type Cue struct {
	CueID    string         `json:"cue_id"`
	Language string         `json:"language"`
	TrackID  int            `json:"track_id"`
	Start    media.Rational `json:"start"`
	End      media.Rational `json:"end"`
	Text     string         `json:"text,omitempty"`
}

type ExpectedCue struct {
	CueID string
	Start media.Rational
	End   media.Rational
}

type TrackInput struct {
	TrackID    int
	Language   string
	TrackType  string
	Blocks     int
	AlignMaxMS float64
	TargetBPS  int64
	ActualBPS  float64
	DeltaPct   float64
	PSNRDB     float64
}

type Request struct {
	AssetID           string
	ContentSHA256     string
	AnalyzerVersion   string
	RequiredLanguages []string
	Duration          media.Rational
	Metrics           Metrics
	Tracks            []TrackInput
	Cues              []Cue
	ExpectedCues      []ExpectedCue
}

type Manifest struct {
	SchemaVersion   int     `json:"schema_version"`
	AssetID         string  `json:"asset_id"`
	StableKey       string  `json:"stable_key"`
	ContentSHA256   string  `json:"content_sha256"`
	AnalyzerVersion string  `json:"analyzer_version"`
	Metrics         Metrics `json:"metrics"`
	Items           []Item  `json:"items"`
	Cues            []Cue   `json:"cues"`
}

func locError(code, msg, language string, trackID int, cueID string) *media.Error {
	e := media.SpecMismatch(code, msg).WithTrack(trackID, 0)
	if language != "" {
		e.Detail = "language=" + language
	}
	if cueID != "" {
		if e.Detail != "" {
			e.Detail += " "
		}
		e.Detail += "cue=" + cueID
	}
	return e
}

// Merge validates and deterministically merges track and cue observations.
// It never returns a partial success manifest when specification checks fail.
func Merge(req Request) (*Manifest, error) {
	if req.AssetID == "" || req.ContentSHA256 == "" {
		return nil, media.SpecMismatch("MANIFEST_IDENTITY_MISSING", "asset id and content digest are required")
	}

	trackByLangType := map[string]TrackInput{}
	for _, t := range req.Tracks {
		lang := t.Language
		if lang == "" {
			lang = "und"
		}
		key := lang + "/" + t.TrackType
		if _, exists := trackByLangType[key]; exists {
			// Two tracks of same language/type are allowed, but the checklist
			// identity must remain unambiguous for acceptance fixtures.
			return nil, locError("MANIFEST_DUPLICATE_TRACK_IDENTITY",
				"multiple tracks share language and type", lang, t.TrackID, "")
		}
		t.Language = lang
		trackByLangType[key] = t
	}

	for _, lang := range req.RequiredLanguages {
		for _, typ := range []string{"audio", "subtitle"} {
			if _, ok := trackByLangType[lang+"/"+typ]; !ok {
				return nil, locError("MANIFEST_MISSING_LANGUAGE_TRACK",
					"required language track is missing", lang, 0, "")
			}
		}
	}

	items := make([]Item, 0, len(req.Tracks))
	for _, t := range req.Tracks {
		lang := t.Language
		if lang == "" {
			lang = "und"
		}
		items = append(items, Item{
			StableKey:  StableKey(req.AssetID, lang, t.TrackType, req.ContentSHA256),
			AssetID:    req.AssetID,
			Language:   lang,
			TrackType:  t.TrackType,
			TrackID:    t.TrackID,
			Blocks:     t.Blocks,
			AlignMaxMS: t.AlignMaxMS,
			TargetBPS:  t.TargetBPS,
			ActualBPS:  t.ActualBPS,
			DeltaPct:   t.DeltaPct,
			PSNRDB:     t.PSNRDB,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StableKey < items[j].StableKey })

	cues, err := mergeCues(req)
	if err != nil {
		return nil, err
	}

	langs := append([]string(nil), req.RequiredLanguages...)
	sort.Strings(langs)
	assetKeyMaterial := req.AssetID + "|" + fmt.Sprint(langs) + "|audio,subtitle,effect|" + req.ContentSHA256
	assetKey := StableKey(assetKeyMaterial, stringsJoin(langs, ","), "bundle", req.ContentSHA256)

	return &Manifest{
		SchemaVersion:   SchemaVersion,
		AssetID:         req.AssetID,
		StableKey:       assetKey,
		ContentSHA256:   req.ContentSHA256,
		AnalyzerVersion: req.AnalyzerVersion,
		Metrics:         req.Metrics,
		Items:           items,
		Cues:            cues,
	}, nil
}

func stringsJoin(xs []string, sep string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += sep
		}
		out += x
	}
	return out
}

func mergeCues(req Request) ([]Cue, error) {
	expected := map[string]ExpectedCue{}
	for _, ec := range req.ExpectedCues {
		expected[ec.CueID] = ec
	}
	seen := map[string]bool{}
	var out []Cue
	for _, cue := range req.Cues {
		if cue.Language == "" {
			cue.Language = "und"
		}
		identity := cue.Language + "/" + cue.CueID
		if seen[identity] {
			continue // repeated delivery or repeated block is idempotent
		}
		seen[identity] = true

		if cue.Start.N < 0 {
			return nil, locError("MANIFEST_CUE_TIME_NEGATIVE", "cue start is before zero", cue.Language, cue.TrackID, cue.CueID)
		}
		if media.Sub(cue.End, cue.Start).N <= 0 {
			return nil, locError("MANIFEST_CUE_BAD_INTERVAL", "cue end must be after start", cue.Language, cue.TrackID, cue.CueID)
		}
		if req.Duration.D > 0 && req.Duration.N > 0 && media.Sub(cue.End, req.Duration).N > 0 {
			return nil, locError("MANIFEST_CUE_OUT_OF_BOUNDS", "cue end exceeds asset duration", cue.Language, cue.TrackID, cue.CueID)
		}
		if exp, ok := expected[cue.CueID]; ok {
			if cue.Start != exp.Start || cue.End != exp.End {
				return nil, locError("MANIFEST_CUE_BOUNDARY_MISMATCH", "cue boundary differs from specification", cue.Language, cue.TrackID, cue.CueID).
					WithDetail(fmt.Sprintf("got=%s..%s want=%s..%s", cue.Start, cue.End, exp.Start, exp.End))
			}
		}
		out = append(out, cue)
	}

	for _, lang := range req.RequiredLanguages {
		for _, ec := range req.ExpectedCues {
			id := lang + "/" + ec.CueID
			if !seen[id] {
				return nil, locError("MANIFEST_CUE_MISSING", "required cue is missing for language", lang, 0, ec.CueID)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start.N != out[j].Start.N {
			return media.ToMilliseconds(out[i].Start) < media.ToMilliseconds(out[j].Start)
		}
		return out[i].Language+out[i].CueID < out[j].Language+out[j].CueID
	})
	return out, nil
}

// Encode returns canonical manifest bytes and their SHA-256.
func Encode(m *Manifest) ([]byte, string, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, "", err
	}
	b = append(b, '\n')
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
