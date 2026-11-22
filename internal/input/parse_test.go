package input

import (
	"encoding/json"
	"testing"

	"dashcheck/internal/model"
)

func validDesc() map[string]any {
	return map[string]any{
		"content_id": "c1",
		"representations": []any{
			map[string]any{
				"id": "hd", "bandwidth_bps": 3000, "width": 1920, "height": 1080,
				"timescale": 1000,
				"segments": []any{
					map[string]any{"number": 1, "start_tick": 0, "duration_tick": 2000, "random_access": true},
					map[string]any{"number": 2, "start_tick": 2000, "duration_tick": 2000, "random_access": true},
				},
			},
		},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func TestParseValid(t *testing.T) {
	d, err := Parse(mustJSON(t, validDesc()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ContentID != "c1" || len(d.Representations[0].Segments) != 2 {
		t.Fatalf("parsed description looks wrong: %+v", d)
	}
}

func TestParseErrorCategories(t *testing.T) {
	rep := func(mut func(map[string]any)) map[string]any {
		d := validDesc()
		r := d["representations"].([]any)[0].(map[string]any)
		mut(r)
		return d
	}
	seg := func(mut func(map[string]any)) map[string]any {
		d := validDesc()
		r := d["representations"].([]any)[0].(map[string]any)
		s := r["segments"].([]any)[1].(map[string]any)
		mut(s)
		return d
	}
	cases := []struct {
		name string
		json any
		code string
	}{
		{"missing content id", map[string]any{"representations": []any{}}, model.CodeMissingField},
		{"no representations", map[string]any{"content_id": "c"}, model.CodeMissingField},
		{"missing rep id", rep(func(r map[string]any) { delete(r, "id") }), model.CodeMissingField},
		{"non-positive bandwidth", rep(func(r map[string]any) { r["bandwidth_bps"] = int64(0) }), model.CodeInvalidValue},
		{"bad dimensions", rep(func(r map[string]any) { r["width"] = int64(0) }), model.CodeInvalidValue},
		{"bad timescale", rep(func(r map[string]any) { r["timescale"] = int64(0) }), model.CodeInvalidValue},
		{"zero duration", seg(func(s map[string]any) { s["duration_tick"] = int64(0) }), model.CodeInvalidValue},
		{"negative start", seg(func(s map[string]any) { s["start_tick"] = int64(-1) }), model.CodeInvalidValue},
		{"duplicate numbers", func() any {
			return seg(func(s map[string]any) {
				s["number"] = int64(1)
				s["start_tick"] = int64(3000)
			})
		}(), model.CodeDuplicate},
		{"non monotonic", seg(func(s map[string]any) { s["start_tick"] = int64(0) }), model.CodeNotMonotonic},
		{"unknown field", func() any {
			d := validDesc()
			d["bogus"] = 1
			return d
		}(), model.CodeInvalidValue},
		{"malformed json", "raw-{not-json", model.CodeInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			if s, ok := tc.json.(string); ok {
				raw = []byte(s)
			} else {
				raw = mustJSON(t, tc.json)
			}
			_, err := Parse(raw)
			me, ok := model.AsError(err)
			if !ok {
				t.Fatalf("expected categorized error, got %v", err)
			}
			if me.Category != model.CategoryInput || me.Code != tc.code {
				t.Fatalf("got %s/%s, want INPUT/%s", me.Category, me.Code, tc.code)
			}
		})
	}
}

func TestParseOverflowEndTick(t *testing.T) {
	d := validDesc()
	r := d["representations"].([]any)[0].(map[string]any)
	r["segments"] = []any{
		map[string]any{
			"number": 1, "start_tick": int64(9223372036854775807),
			"duration_tick": 2, "random_access": true,
		},
	}
	_, err := Parse(mustJSON(t, d))
	me, ok := model.AsError(err)
	if !ok || me.Code != model.CodeOverflow {
		t.Fatalf("expected OVERFLOW, got %v", err)
	}
}
