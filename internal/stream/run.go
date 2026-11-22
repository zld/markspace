package stream

import (
	"context"
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/index"
)

// Sources bundles the open handles a run needs.
type Sources struct {
	MP4       *os.File
	IndexFile *os.File
	Header    index.Header
}

// Run executes batches from a resumable checkpoint. The output path is
// checked against the MP4 path so the source can never be overwritten.
func Run(ctx context.Context, cfg Config, src Sources, cpPath, outputPath, mp4Path string,
	consumer Consumer) (*Counters, *Checkpoint, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	if samePath(outputPath, mp4Path) {
		return nil, nil, clock.ErrInvalidArgument("stream: output path must differ from source MP4 path")
	}
	cp, err := LoadCheckpoint(cpPath, digestOfHeader(src.Header))
	if err != nil {
		return nil, nil, err
	}
	if cp.NextSample > src.Header.SampleCount {
		return nil, nil, clock.ErrRecoverablef("stream.run", "checkpoint next_sample %d beyond count %d",
			cp.NextSample, src.Header.SampleCount)
	}

	sem := NewSemaphore(cfg.MaxInFlightBytes)
	oh := newOutputHash()
	counters := Counters{}

	for cp.NextSample < src.Header.SampleCount {
		if err := ctx.Err(); err != nil {
			return &counters, cp, clock.ErrCanceled("stream.run", err)
		}
		batch, physBytes, err := readBatch(ctx, sem, cfg, src, cp.NextSample)
		if err != nil {
			return &counters, cp, err
		}
		logical, err := consumer.Consume(ctx, batch)
		if err != nil {
			return &counters, cp, err
		}
		// Accounting and digest happen only after a successful consume, so
		// a failed write leaves the checkpoint pointing at the retry start.
		for _, rv := range batch.Records {
			oh.Write(rv)
		}
		sem.Release(batch.reserved)
		counters.BatchesEmitted++
		counters.SamplesEmitted += uint64(len(batch.Records))
		counters.BytesRead += physBytes
		counters.LogicalBytes += logical
		cp.NextSample = batch.FirstSample + uint32(len(batch.Records))
		cp.SamplesDone = counters.SamplesEmitted
		cp.BytesRead = counters.BytesRead
		cp.LogicalBytes = counters.LogicalBytes
		cp.BatchesDone = counters.BatchesEmitted
		cp.OutputDigest = oh.Digest()
		if err := cp.Save(cpPath); err != nil {
			return &counters, cp, err
		}
	}
	counters.PeakInFlight = sem.Peak()
	return &counters, cp, nil
}

// readBatch assembles one batch under both caps, acquiring byte budget
// before any mdat read. Budget is accounted in *logical wire bytes* (the
// network payload size that drives real memory pressure); every offset/size
// used for the physical read comes from a verified index record and is
// re-checked against the file before reading.
func readBatch(ctx context.Context, sem *Semaphore, cfg Config, src Sources,
	start uint32) (Batch, int64, error) {
	var (
		batch      Batch
		physBytes  int64
		wireBytes  int64
	)
	batch.FirstSample = start
	i := start
	for i < src.Header.SampleCount && len(batch.Records) < cfg.MaxBatchSamples {
		rec, err := index.ReadRecord(src.IndexFile, src.Header, i)
		if err != nil {
			return batch, 0, err
		}
		phys := int64(rec.Size)
		wire := int64(rec.WireSize)
		if wireBytes > 0 && wireBytes+wire > cfg.MaxBatchBytes {
			break
		}
		if err := sem.Acquire(ctx, wire); err != nil {
			return batch, 0, err
		}
		payload, err := readPayload(src.MP4, rec)
		if err != nil {
			sem.Release(wire)
			return batch, 0, err
		}
		batch.Records = append(batch.Records, RecordView{
			SampleIndex: i,
			Offset:      rec.Offset,
			Size:        rec.Size,
			WireSize:    rec.WireSize,
			DTSNanos:    rec.DTSNanos,
			CTSNanos:    rec.CTSNanos,
			Sync:        rec.IsSync(),
			Payload:     payload,
		})
		physBytes += phys
		wireBytes += wire
		i++
	}
	batch.Index = int(start) / cfg.MaxBatchSamples
	// Return the wire reservation for semaphore release; physical bytes are
	// reported separately in counters.
	batch.reserved = wireBytes
	return batch, physBytes, nil
}

func readPayload(f *os.File, rec index.Record) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, clock.ErrRecoverablef("stream.read", "stat: %v", err)
	}
	end := rec.Offset + uint64(rec.Size)
	if end > uint64(fi.Size()) || end < rec.Offset {
		return nil, clock.WithTrack(clock.ErrRecoverablef("stream.read",
			"record payload [%d,%d) outside file", rec.Offset, end), 0, int64(rec.Offset))
	}
	buf := make([]byte, rec.Size)
	if _, err := f.ReadAt(buf, int64(rec.Offset)); err != nil {
		return nil, clock.ErrRecoverablef("stream.read", "readAt %d: %v", rec.Offset, err)
	}
	return buf, nil
}

func digestOfHeader(h index.Header) string {
	return hex32(h.InputDigest[:])
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return os.SameFile(ai, bi)
}
