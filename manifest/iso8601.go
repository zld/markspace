package manifest

import (
	"fmt"
	"strings"

	"mediamux/media"
)

// parseISODuration parses ISO-8601 durations such as PT6.006S or PT1H2M3.5S.
func parseISODuration(s string) (media.Ratio, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "P") {
		return media.Ratio{}, fmt.Errorf("not an ISO-8601 duration: %q", s)
	}
	body := strings.TrimPrefix(s, "P")
	total := media.Ratio{}
	sawField := false
	for body != "" {
		if body[0] == 'T' {
			body = body[1:]
			continue
		}
		i := 0
		for i < len(body) && (body[i] >= '0' && body[i] <= '9' || body[i] == '.') {
			i++
		}
		if i == 0 || i == len(body) {
			return media.Ratio{}, fmt.Errorf("invalid ISO-8601 duration: %q", s)
		}
		value, err := media.ParseDecimalSeconds(body[:i])
		if err != nil {
			return media.Ratio{}, err
		}
		var mult int64
		switch body[i] {
		case 'D':
			mult = 86400
		case 'H':
			mult = 3600
		case 'M':
			mult = 60
		case 'S':
			mult = 1
		default:
			return media.Ratio{}, fmt.Errorf("unsupported ISO-8601 unit %q", body[i])
		}
		total = total.Add(value.MulInt(mult))
		sawField = true
		body = body[i+1:]
	}
	if !sawField {
		return media.Ratio{}, fmt.Errorf("empty ISO-8601 duration: %q", s)
	}
	return total, nil
}
