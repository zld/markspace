package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parseFlags parses --key value arguments; boolean corruption flags are
// value-less. Any non-flag token is rejected so typos cannot be ignored.
func parseFlags(args []string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		key := strings.TrimPrefix(a, "--")
		if key == "" {
			return nil, fmt.Errorf("empty flag name")
		}
		if _, seen := out[key]; seen {
			return nil, fmt.Errorf("flag --%s given more than once", key)
		}
		switch key {
		case "corrupt-index", "corrupt-stream":
			out[key] = "true"
		default:
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s needs a value", key)
			}
			out[key] = args[i+1]
			i++
		}
	}
	return out, nil
}

func atoiFlag(flags map[string]string, key string, def int) (int, error) {
	v, ok := flags[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("--%s must be an integer, got %q", key, v)
	}
	return n, nil
}
