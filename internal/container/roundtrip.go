package container

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/abema/go-mp4"
)

// BoxRoundtrip is the field consistency result for one parsed box.
type BoxRoundtrip struct {
	Type            string   `json:"type"`
	TrackIndex      int      `json:"track_index"`
	Offset          int64    `json:"byte_offset"`
	FieldsTotal     int      `json:"fields_total"`
	FieldsMatched   int      `json:"fields_matched"`
	Mismatches      []string `json:"mismatches,omitempty"`
	MarshalVerified bool     `json:"marshal_verified"`
}

// RoundtripReport summarizes parse -> serialize -> parse consistency.
type RoundtripReport struct {
	Boxes         []BoxRoundtrip `json:"boxes"`
	BoxCount      int            `json:"box_count"`
	FieldsTotal   int            `json:"fields_total"`
	FieldsMatched int            `json:"fields_matched"`
	Rate          float64        `json:"rate"`
}

// VerifyRoundtrip serializes every supported parsed payload with go-mp4 and
// compares the reparsed fields.
func VerifyRoundtrip(f *File) (*RoundtripReport, error) {
	report := &RoundtripReport{}
	for _, b := range f.Boxes {
		if b.Payload == nil {
			continue
		}
		br := BoxRoundtrip{Type: b.Type, TrackIndex: b.TrackIndex, Offset: b.Offset}
		var buf bytes.Buffer
		if _, err := mp4.Marshal(&buf, b.Payload, mp4.Context{}); err != nil {
			return nil, fmt.Errorf("marshal %s at %d: %w", b.Type, b.Offset, err)
		}
		br.MarshalVerified = buf.Len() > 0 || b.Size == int64(b.HeaderSize)
		parsed, _, err := mp4.UnmarshalAny(bytes.NewReader(buf.Bytes()), mp4.StrToBoxType(b.Type), uint64(buf.Len()), mp4.Context{})
		if err != nil {
			return nil, fmt.Errorf("unmarshal roundtrip %s at %d: %w", b.Type, b.Offset, err)
		}
		compareFields(reflect.ValueOf(b.Payload).Elem(), reflect.ValueOf(parsed).Elem(), b.Type, &br)
		report.Boxes = append(report.Boxes, br)
		report.BoxCount++
		report.FieldsTotal += br.FieldsTotal
		report.FieldsMatched += br.FieldsMatched
	}
	if report.FieldsTotal > 0 {
		report.Rate = float64(report.FieldsMatched) / float64(report.FieldsTotal)
		if report.Rate > 1 {
			report.Rate = 1
		}
	} else {
		report.Rate = 1
	}
	return report, nil
}

func compareFields(a, b reflect.Value, path string, br *BoxRoundtrip) {
	if !a.IsValid() || !b.IsValid() {
		br.FieldsTotal++
		if reflect.DeepEqual(valueInterface(a), valueInterface(b)) {
			br.FieldsMatched++
		} else {
			br.Mismatches = append(br.Mismatches, path)
		}
		return
	}

	switch a.Kind() {
	case reflect.Ptr, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			br.FieldsTotal++
			if a.IsNil() == b.IsNil() {
				br.FieldsMatched++
			} else {
				br.Mismatches = append(br.Mismatches, path+".nil")
			}
			return
		}
		compareFields(a.Elem(), b.Elem(), path, br)
	case reflect.Struct:
		typ := a.Type()
		for i := 0; i < typ.NumField(); i++ {
			sf := typ.Field(i)
			if sf.PkgPath != "" && !sf.Anonymous {
				continue
			}
			compareFields(a.Field(i), b.Field(i), path+"."+sf.Name, br)
		}
	case reflect.Slice, reflect.Array:
		if a.Kind() == reflect.Slice && a.IsNil() {
			br.FieldsTotal++
			if b.Kind() == reflect.Slice && b.IsNil() {
				br.FieldsMatched++
			} else if b.Len() == 0 {
				br.FieldsMatched++
			} else {
				br.Mismatches = append(br.Mismatches, path+".slice-nil")
			}
			return
		}
		n := a.Len()
		for i := 0; i < n; i++ {
			if i >= b.Len() {
				br.FieldsTotal++
				br.Mismatches = append(br.Mismatches, fmt.Sprintf("%s[%d]", path, i))
				continue
			}
			compareFields(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i), br)
		}
	default:
		br.FieldsTotal++
		av := valueInterface(a)
		bv := valueInterface(b)
		if reflect.DeepEqual(av, bv) {
			br.FieldsMatched++
		} else {
			br.Mismatches = append(br.Mismatches, fmt.Sprintf("%s: %#v != %#v", path, av, bv))
		}
	}
}

func valueInterface(v reflect.Value) any {
	if !v.IsValid() || !v.CanInterface() {
		return nil
	}
	return v.Interface()
}
