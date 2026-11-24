package main

import (
	"fmt"
	"strconv"
)

// flags is a minimal --key value / --key=value parser, sufficient for the
// fixed command surface and free of third-party flag framework dependencies.
type flags struct {
	vals map[string]string
	pos  []string
}

func parseFlags(args []string) (*flags, error) {
	f := &flags{vals: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			f.pos = append(f.pos, a) // positional token, e.g. demo|corrupt
			continue
		}
		key := a
		var val string
		if eq := indexByte(a, '='); eq >= 0 {
			key, val = a[:eq], a[eq+1:]
		} else {
			if i+1 >= len(args) {
				return nil, badArgs("flag %s needs a value", key)
			}
			val = args[i+1]
			i++
		}
		f.vals[key] = val
	}
	return f, nil
}

// positional returns the collected non-flag tokens.
func (f *flags) positional() []string { return f.pos }

func (f *flags) get(key, def string) string {
	if v, ok := f.vals[key]; ok {
		return v
	}
	return def
}

func (f *flags) int(key string, def int) (int, error) {
	v, ok := f.vals[key]
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, badArgs("flag %s needs an integer, got %q", key, v)
	}
	return n, nil
}

func (f *flags) float(key string, def float64) (float64, error) {
	v, ok := f.vals[key]
	if !ok {
		return def, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, badArgs("flag %s needs a number, got %q", key, v)
	}
	return n, nil
}

func (f *flags) require(keys ...string) error {
	for _, k := range keys {
		if _, ok := f.vals[k]; !ok {
			return fmt.Errorf("%w: missing required flag %s", errBadArgs, k)
		}
	}
	return nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
