package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteJSON emits indented JSON with a trailing newline. Slices are already
// sorted by the builder, so repeated runs on the same input are byte-identical.
func WriteJSON(w io.Writer, s *Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(s)
}

// EncodeJSON returns the summary as bytes, useful for tests.
func EncodeJSON(s *Summary) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteText emits a human-readable summary for terminal use.
func WriteText(w io.Writer, s *Summary) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "input:\t%s\n", s.InputPath)
	fmt.Fprintf(tw, "packets:\t%d (%d bytes, prefix %d, trailing %d bytes)\n", s.Packets, s.PacketBytes, s.PrefixOffset, s.TrailingBytes)
	patVersion := "-"
	if s.PATVersion != nil {
		patVersion = fmt.Sprintf("%d", *s.PATVersion)
	}
	fmt.Fprintf(tw, "PAT:\tversion %s, version changes %d, duplicates %d\n", patVersion, s.PATVersionChanges, s.PATDuplicates)
	fmt.Fprintf(tw, "PMT:\tversion changes %d, duplicates %d\n", s.PMTVersionChanges, s.PMTDuplicates)
	fmt.Fprintf(tw, "PSI sections:\tparsed %d, crc errors %d, length errors %d, discarded %d\n", s.Sections, s.SectionCRCErrors, s.SectionLengthErrors, s.SectionsDiscarded)
	fmt.Fprintf(tw, "continuity:\terrors %d, duplicates %d, discontinuities %d, transport errors %d, lost %d\n", s.ContinuityErrors, s.Duplicates, s.Discontinuities, s.TransportErrors, s.LostPackets)
	fmt.Fprintf(tw, "programs:\t%d\n", len(s.Programs))
	for _, p := range s.Programs {
		state := "pending PMT"
		if p.Active {
			state = "active"
		}
		fmt.Fprintf(tw, "  program %d:\tPMT PID 0x%04x, PCR PID 0x%04x, PMT version %d, %s\n", p.ProgramNumber, p.PMTPID, p.PCRPID, p.PMTVersion, state)
	}
	if len(s.Streams) > 0 {
		fmt.Fprintf(tw, "streams:\n")
		for _, sr := range s.Streams {
			fmt.Fprintf(tw, "  program %d\tpid 0x%04x\ttype %s (%s)\tpackets %d\tcc errors %d\n",
				sr.ProgramNumber, sr.PID, sr.StreamTypeHex, sr.Description, sr.PacketCount, sr.ContinuityErrors)
		}
	}
	return tw.Flush()
}
