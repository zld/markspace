// Package fault: fault accumulation, counting and error-chain extraction.
package fault

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// List accumulates faults in observation order.
type List struct {
	Items []*Fault `json:"faults"`
}

func (l *List) Add(f *Fault) *Fault {
	if l == nil || f == nil {
		return f
	}
	l.Items = append(l.Items, f)
	return f
}

// Addf is a convenience wrapper around Add and New.
func (l *List) Addf(stage Stage, kind Kind, format string, args ...interface{}) *Fault {
	return l.Add(New(stage, kind, format, args...))
}

func (l *List) Len() int {
	if l == nil {
		return 0
	}
	return len(l.Items)
}

func (l *List) Error() string {
	if l.Len() == 0 {
		return "no faults"
	}
	parts := make([]string, 0, len(l.Items))
	for _, f := range l.Items {
		parts = append(parts, f.Error())
	}
	return strings.Join(parts, "; ")
}

// Recoverable reports true when every recorded fault allows processing to
// continue.  An empty list is recoverable.
func (l *List) Recoverable() bool {
	if l == nil {
		return true
	}
	for _, f := range l.Items {
		if !f.Recoverable {
			return false
		}
	}
	return true
}

// CountByKind returns stable, sorted kind counts.
func (l *List) CountByKind() []KindCount {
	if l == nil {
		return nil
	}
	counts := map[Kind]int{}
	for _, f := range l.Items {
		counts[f.Kind]++
	}
	out := make([]KindCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, KindCount{Kind: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

type KindCount struct {
	Kind  Kind `json:"kind"`
	Count int  `json:"count"`
}

// Merge appends all faults from another list.
func (l *List) Merge(other *List) {
	if other == nil {
		return
	}
	l.Items = append(l.Items, other.Items...)
}

// AsList extracts a *List from an error chain.
func AsList(err error) (*List, bool) {
	var l *List
	if errors.As(err, &l) {
		return l, true
	}
	return nil, false
}

// MarshalJSON keeps the wire shape stable.
func (l *List) MarshalJSON() ([]byte, error) {
	type alias List
	return json.Marshal((*alias)(l))
}

// FirstNonRecoverable returns the first fault that forbids continuation.
func (l *List) FirstNonRecoverable() *Fault {
	if l == nil {
		return nil
	}
	for _, f := range l.Items {
		if !f.Recoverable {
			return f
		}
	}
	return nil
}

// Merge is nil-safe on the receiver as well.
func Merge(lists ...*List) *List {
	out := &List{}
	for _, l := range lists {
		out.Merge(l)
	}
	return out
}
