package rtp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// EventRecord is one line of the JSONL event log. Datagram is the raw RTP
// packet (base64 on disk so the log stays pure text); ArrivalNS is the
// wall-clock arrival on the shared nanosecond base.
type EventRecord struct {
	Index     int    `json:"index"`
	ArrivalNS int64  `json:"arrival_ns"`
	Datagram  string `json:"datagram_b64"`
}

// EventSource yields RTP events in arrival order.
type EventSource struct {
	file    *os.File
	reader  *bufio.Reader
	index   int
	path    string
	maxSize int64
	read    int64
}

// OpenEventLog opens a JSONL event file and enforces the file-size limit.
func OpenEventLog(path string, limits model.Limits) (*EventSource, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, model.NewError(model.ClassCorruptBox, "stat event log %s: %v", path, err)
	}
	if fi.Size() > limits.MaxFileBytes {
		return nil, model.AtError(model.ClassLimitExceeded, path, 0, 0,
			"event log %d bytes exceeds limit %d", fi.Size(), limits.MaxFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.ClassCorruptBox, "open event log %s: %v", path, err)
	}
	return &EventSource{
		file:    f,
		reader:  bufio.NewReader(f),
		path:    path,
		maxSize: limits.MaxFileBytes,
	}, nil
}

// Next reads one event. io.EOF marks a clean end; malformed lines return a
// structured ClassRTP error carrying the line offset.
func (s *EventSource) Next() (*EventRecord, *rtp.Packet, error) {
	line, err := s.reader.ReadString('\n')
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 0 {
		s.read += int64(len(line))
		if s.read > s.maxSize {
			return nil, nil, model.AtError(model.ClassLimitExceeded, s.path, 0, s.read,
				"event log exceeds %d bytes while streaming", s.maxSize)
		}
		s.index++
		var rec EventRecord
		if jerr := json.Unmarshal([]byte(trimmed), &rec); jerr != nil {
			return nil, nil, model.AtError(model.ClassRTP, s.path, 0, s.read,
				"line %d is not valid JSON: %v", s.index, jerr)
		}
		raw, derr := base64.StdEncoding.DecodeString(rec.Datagram)
		if derr != nil {
			return nil, nil, model.AtError(model.ClassRTP, s.path, 0, s.read,
				"line %d datagram is not base64: %v", s.index, derr)
		}
		var pkt rtp.Packet
		if perr := pkt.Unmarshal(raw); perr != nil {
			return &rec, nil, model.AtError(model.ClassRTP, s.path, 0, s.read,
				"line %d invalid RTP packet: %v", s.index, perr)
		}
		rec.Index = s.index
		return &rec, &pkt, nil
	}
	if err == nil {
		return nil, nil, nil // blank line: keep reading
	}
	return nil, nil, err
}

// Close releases the underlying file.
func (s *EventSource) Close() error { return s.file.Close() }

// WriteEventLog serializes raw datagrams as a JSONL event log.
func WriteEventLog(path string, arrivals []model.Ticks, datagrams [][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i, dg := range datagrams {
		rec := EventRecord{
			Index:     i + 1,
			ArrivalNS: int64(arrivals[i]),
			Datagram:  base64.StdEncoding.EncodeToString(dg),
		}
		if err := enc.Encode(&rec); err != nil {
			return err
		}
	}
	return nil
}
