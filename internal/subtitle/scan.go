package subtitle

import (
	"strings"

	"archivecheck/internal/fault"
)

// lineScanner yields lines with their original byte offsets.
type textLine struct {
	Text string
	Off  int64
	End  int64
}

type lineScanner struct {
	data []byte
	off  int64
}

func (p *lineScanner) done() bool { return p.off >= int64(len(p.data)) }

func (p *lineScanner) peekBlank() bool {
	if p.done() {
		return false
	}
	i := strings.IndexByte(string(p.data[p.off:]), '\n')
	if i < 0 {
		return strings.TrimSpace(string(p.data[p.off:])) == ""
	}
	return strings.TrimSpace(string(p.data[p.off:p.off+int64(i)])) == ""
}

func (p *lineScanner) next() textLine {
	start := p.off
	rest := p.data[p.off:]
	i := strings.IndexByte(string(rest), '\n')
	var raw []byte
	if i < 0 {
		raw = rest
		p.off = int64(len(p.data))
	} else {
		raw = rest[:i]
		p.off += int64(i) + 1
	}
	end := p.off
	return textLine{Text: strings.TrimSuffix(string(raw), "\r"), Off: start, End: end}
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func clip(s string) string {
	const max = 40
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// located rebases an error produced while parsing line at base inside src, so
// the reported offset is absolute within the original file.
func located(err error, src string, base int64) error {
	if fe := fault.As(err); fe != nil {
		fe.Source = src
		fe.Offset += base
		fe.End += base
		return fe
	}
	return fault.Wrap(err, fault.StageSubtitle, fault.KindCorrupt, src, base, "subtitle parse")
}
