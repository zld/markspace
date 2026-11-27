package mediacore

import (
	"encoding/xml"
	"io"
)

// startElement is re-exported so playlist.go can type-assert without taking a
// direct dependency on encoding/xml token types.
type startElement = xml.StartElement

// xmlDecoder wraps encoding/xml.Decoder while exposing the raw byte input
// offset, which the standard decoder tracks for every token.
type xmlDecoder struct {
	d *xml.Decoder
}

func newXMLDecoder(r io.Reader) *xmlDecoder {
	return &xmlDecoder{d: xml.NewDecoder(r)}
}

func (x *xmlDecoder) Token() (xml.Token, error) { return x.d.Token() }

// InputOffset returns the decoder byte position at the most recent token.
func (x *xmlDecoder) InputOffset() int64 { return x.d.InputOffset() }

// attrVal looks up an attribute by local name; "" when absent.
func attrVal(se startElement, name string) string {
	for i := range se.Attr {
		if se.Attr[i].Name.Local == name {
			return se.Attr[i].Value
		}
	}
	return ""
}

// attrMap materializes all attributes as a lookup map (last one wins).
func attrMap(se startElement) map[string]string {
	out := make(map[string]string, len(se.Attr))
	for i := range se.Attr {
		out[se.Attr[i].Name.Local] = se.Attr[i].Value
	}
	return out
}
