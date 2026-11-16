package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Fault describes one injected defect in a published profile.
type Fault struct {
	Kind    string // "drop", "swap", "truncate", "tamper"
	Profile int    // bitrate kbps identifying the profile directory
	A, B    int    // segment indices
}

// ParseFault parses "<profile>:<a>[,<b>]" for the given fault kind.
func ParseFault(kind, value string) (Fault, error) {
	f := Fault{Kind: kind}
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return f, fmt.Errorf("fault --%s: want <profile>:<a>[,<b>], got %q", kind, value)
	}
	var err error
	if f.Profile, err = strconv.Atoi(parts[0]); err != nil {
		return f, fmt.Errorf("fault --%s: bad profile: %w", kind, err)
	}
	idx := strings.Split(parts[1], ",")
	if f.A, err = strconv.Atoi(idx[0]); err != nil {
		return f, fmt.Errorf("fault --%s: bad index: %w", kind, err)
	}
	f.B = f.A
	if len(idx) == 2 {
		if f.B, err = strconv.Atoi(idx[1]); err != nil {
			return f, fmt.Errorf("fault --%s: bad index: %w", kind, err)
		}
	}
	if kind == "swap" && f.A == f.B {
		return f, fmt.Errorf("fault --swap: need two distinct indices")
	}
	return f, nil
}

// FaultFlag binds one fault-injecting CLI flag (flag.Value).
type FaultFlag struct {
	List *[]Fault
	Kind string
}

// String implements flag.Value.
func (f FaultFlag) String() string { return "" }

// Set parses and records one flag occurrence.
func (f FaultFlag) Set(v string) error {
	fl, err := ParseFault(f.Kind, v)
	if err != nil {
		return err
	}
	*f.List = append(*f.List, fl)
	return nil
}

// Apply injects the fault into the published profile directory under
// root (the directory named after the profile's bitrate).
func Apply(root string, f Fault) error {
	dir := filepath.Join(root, strconv.Itoa(f.Profile))
	name := func(i int) string { return filepath.Join(dir, fmt.Sprintf("seg_%05d.ts", i)) }
	switch f.Kind {
	case "drop":
		return os.Remove(name(f.A))
	case "swap":
		a, b := name(f.A), name(f.B)
		tmp := a + ".swaptmp"
		if err := os.Rename(a, tmp); err != nil {
			return err
		}
		if err := os.Rename(b, a); err != nil {
			os.Rename(tmp, a)
			return err
		}
		return os.Rename(tmp, b)
	case "truncate":
		fi, err := os.Stat(name(f.A))
		if err != nil {
			return err
		}
		return os.Truncate(name(f.A), fi.Size()/2)
	case "tamper":
		fh, err := os.OpenFile(name(f.A), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer fh.Close()
		buf := make([]byte, 1)
		if _, err := fh.ReadAt(buf, 1000); err != nil {
			return err
		}
		buf[0] ^= 0xFF
		_, err = fh.WriteAt(buf, 1000)
		return err
	}
	return fmt.Errorf("unknown fault kind %q", f.Kind)
}
