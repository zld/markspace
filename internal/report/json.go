// Package report renders audit summaries as deterministic JSON and as a
// greppable key=value text report. Field names and array orders are part of
// the tool's contract and stay stable across runs.
package report

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/example/tsaudit/internal/analyze"
	"github.com/example/tsaudit/internal/program"
	"github.com/example/tsaudit/internal/psi"
)

// JSON writes the summary as indented JSON with a trailing newline.
func JSON(w io.Writer, sum *analyze.Summary) error {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sum); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// Text writes the human/script-friendly key=value summary. One fact per line
// keeps shell assertions independent of column layout.
func Text(w io.Writer, sum *analyze.Summary) error {
	b := &bytes.Buffer{}
	kv(b, "source", sum.Source)
	kv(b, "total_packets", strconv.FormatInt(sum.Transport.TotalPackets, 10))
	kv(b, "sync_errors", strconv.FormatInt(sum.Transport.SyncErrors, 10))
	kv(b, "truncated_tail_bytes", strconv.Itoa(sum.Transport.TruncatedTailBytes))
	kv(b, "transport_error_packets", strconv.FormatInt(sum.Transport.TransportErrorPackets, 10))
	kv(b, "pat_version_changes", strconv.Itoa(sum.PATVersionChanges))
	kv(b, "pmt_version_changes", strconv.Itoa(sum.PMTVersionChanges))
	kv(b, "pat_wraps", strconv.Itoa(sum.PATWraps))
	kv(b, "pmt_wraps", strconv.Itoa(sum.PMTWraps))
	kv(b, "published_versions", strconv.Itoa(len(sum.PublishedVersions)))
	kv(b, "mapping_records", strconv.Itoa(len(sum.Mappings)))
	kv(b, "corrupt_sections", strconv.Itoa(len(sum.CorruptSections)))
	kv(b, "has_complete_pat", strconv.FormatBool(sum.HasCompletePAT))
	kv(b, "missing_pmts", joinU16(sum.MissingPMTs))
	kv(b, "continuity_gaps", strconv.Itoa(totalGaps(sum)))
	kv(b, "exit_code", strconv.Itoa(sum.ExitCode))

	for _, pub := range sum.PublishedVersions {
		writePublication(b, pub)
	}
	for _, m := range sum.Mappings {
		writeMapping(b, m)
	}
	for _, c := range sum.PIDCounters {
		writePIDCounters(b, c)
	}
	for _, c := range sum.CorruptSections {
		writeCorrupt(b, c)
	}
	_, err := w.Write(b.Bytes())
	return err
}

func writePublication(b *bytes.Buffer, pub psi.Publication) {
	table := "PAT"
	if pub.TableID == psi.TableIDPMT {
		table = "PMT"
	}
	kv(b, "published", table+" pid="+strconv.Itoa(int(pub.PID))+
		" version="+strconv.Itoa(int(pub.Version)))
}

func writeMapping(b *bytes.Buffer, m program.MappingRecord) {
	kv(b, "mapping",
		"program="+strconv.Itoa(int(m.ProgramNumber))+
			" pmt_pid="+strconv.Itoa(int(m.PMTPID))+
			" elementary_pid="+strconv.Itoa(int(m.ElementaryPID))+
			" stream_type=0x"+hex2(m.StreamType)+
			" version="+strconv.Itoa(int(m.FirstVersion))+
			" packets="+strconv.FormatInt(m.FirstPacket, 10)+
			".."+strconv.FormatInt(m.LastPacket, 10))
}

func writePIDCounters(b *bytes.Buffer, c analyze.PIDCounters) {
	kv(b, "pid",
		strconv.Itoa(int(c.PID))+
			" total="+strconv.FormatInt(c.TotalPackets, 10)+
			" valid="+strconv.Itoa(c.ValidPackets)+
			" duplicate="+strconv.Itoa(c.Duplicates)+
			" continuity_gaps="+strconv.Itoa(c.ContinuityGaps)+
			" gap_events="+strconv.Itoa(c.GapEvents)+
			" errors="+strconv.Itoa(c.ErrorPackets))
}

func writeCorrupt(b *bytes.Buffer, c psi.SectionCandidate) {
	kv(b, "corrupt_section",
		"pid="+strconv.Itoa(int(c.PID))+
			" table_id=0x"+hex2(c.TableID)+
			" version="+strconv.Itoa(c.Version)+
			" reason="+c.Reason)
}

func totalGaps(sum *analyze.Summary) int {
	total := 0
	for _, c := range sum.PIDCounters {
		total += c.ContinuityGaps
	}
	return total
}

func joinU16(vs []uint16) string {
	if len(vs) == 0 {
		return ""
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = strconv.Itoa(int(v))
	}
	return strings.Join(parts, ",")
}

func hex2(v uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4], digits[v&0x0f]})
}

func kv(b *bytes.Buffer, key, value string) {
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(value)
	b.WriteByte('\n')
}
