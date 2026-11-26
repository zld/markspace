package manifest

import (
	"bytes"
	"encoding/xml"
	"io"
	"sort"

	"medtrace/internal/core"
)

// node is a minimal, line-annotated XML element. DASH documents are small
// enough that materialising the tree is simpler and more precise than struct
// decoding, which cannot report the line of a offending element.
type node struct {
	name     string // local name, namespace ignored
	attrs    map[string]string
	line     int
	children []*node
}

// attr returns the attribute value and whether it was present.
func (n *node) attr(name string) (string, bool) {
	v, ok := n.attrs[name]
	return v, ok
}

// child returns the first direct child with the given local name.
func (n *node) child(name string) *node {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// childrenNamed returns every direct child with the given local name.
func (n *node) childrenNamed(name string) []*node {
	var out []*node
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// lineOf returns a 1-based line number for a byte offset in data.
func lineOf(newlines []int, off int64) int {
	i := sort.Search(len(newlines), func(i int) bool { return int64(newlines[i]) >= off })
	return i + 1
}

// newlineOffsets indexes the position of every '\n'.
func newlineOffsets(data []byte) []int {
	var out []int
	for i, b := range data {
		if b == '\n' {
			out = append(out, i)
		}
	}
	return out
}

// buildTree parses XML into a node tree, attaching the line of each element's
// start tag. A syntax error is reported with its own line when encoding/xml
// provides one.
func buildTree(rel string, data []byte) (*node, *core.Fault) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	newlines := newlineOffsets(data)
	var root *node
	var stack []*node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			line := 1
			if se, ok := err.(*xml.SyntaxError); ok && se.Line > 0 {
				line = se.Line
			} else {
				line = lineOf(newlines, dec.InputOffset())
			}
			return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: line, Byte: -1}, "xml", err.Error())
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name.Local, attrs: make(map[string]string, len(t.Attr)), line: lineOf(newlines, dec.InputOffset())}
			for _, a := range t.Attr {
				if _, dup := n.attrs[a.Name.Local]; dup {
					continue
				}
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: n.line, Byte: -1}, "xml", "multiple root elements")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: lineOf(newlines, dec.InputOffset()), Byte: -1}, "xml", "unexpected closing tag")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: 1, Byte: -1}, "xml", "no root element")
	}
	if len(stack) != 0 {
		return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: root.line, Byte: -1}, "xml", "unclosed element")
	}
	return root, nil
}
