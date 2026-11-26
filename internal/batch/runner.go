package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"image"
	"io"
	"os"
	"path/filepath"

	"mediarepair/internal/cache"
	"mediarepair/internal/fsx"
	"mediarepair/internal/index"
	"mediarepair/internal/media"
	"mediarepair/internal/probe"
	"mediarepair/internal/repair"
	"mediarepair/internal/source"
)

type Options struct {
	Root                   string
	Manifest               *source.Manifest
	BlockSize              int
	Cache                  *cache.Cache
	InterruptAfterBlocks   int
	CrashBeforeCommitBlock int
}

func Run(opts Options) (*Result, error) {
	root := opts.Root
	m := opts.Manifest
	if m == nil {
		loaded, err := source.ReadManifest(filepath.Join(root, "manifest.json"))
		if err != nil {
			return nil, err
		}
		m = loaded
	}
	blockSize := opts.BlockSize
	if blockSize <= 0 {
		blockSize = 4
	}
	// The zero Options value means "disabled"; callers that need the first
	// block can use a negative? No: first-block injection is not required.
	crashBefore := opts.CrashBeforeCommitBlock
	if crashBefore == 0 {
		crashBefore = -1
	}

	inputFP, err := cache.InputFingerprint(m)
	if err != nil {
		return nil, err
	}
	metadataFP, err := cache.MetadataFingerprint(m)
	if err != nil {
		return nil, err
	}

	cp, err := LoadCheckpoint(root)
	if err != nil {
		return nil, err
	}
	resumed := cp != nil && !cp.Complete
	if cp != nil && cp.InputFingerprint != inputFP {
		if !cp.Complete {
			return nil, media.E(media.CodeInputChanged, "batch.resume", nil).
				WithFile("manifest.json").WithOffset(-1)
		}
		// A completed revision may be superseded. Its package history stays
		// untouched, but block/index progress starts over.
		cp = nil
	}
	if cp != nil {
		if opts.BlockSize > 0 && cp.BlockSize != opts.BlockSize {
			return nil, media.E(media.CodeInvalidArgument, "batch.block_size", nil)
		}
		blockSize = cp.BlockSize
	}
	if opts.Cache != nil {
		opts.Cache.RequireIndexRebuild(inputFP)
	}

	if err := source.ValidateFrames(root, m); err != nil {
		return nil, err
	}

	var st *source.SampleTable
	packagingFP := ""
	if m.MP4 != nil {
		mp4Full, err := fsx.SafeJoin(root, m.MP4.Path)
		if err != nil {
			return nil, err
		}
		packagingFP, err = cache.FileSHA256(mp4Full)
		if err != nil {
			return nil, media.E(media.CodeWrite, "mp4.fingerprint", err).WithFile(m.MP4.Path)
		}
		if opts.Cache != nil {
			if cached, ok := opts.Cache.GetPackaging(packagingFP); ok {
				st = cached
			}
		}
		if st == nil {
			st, err = source.ReadSampleTable(mp4Full, uint32(m.Track))
			if err != nil {
				return nil, err
			}
			if opts.Cache != nil {
				opts.Cache.PutPackaging(packagingFP, st)
			}
		}
	}

	committed := 0
	if cp != nil {
		committed = cp.CommittedBlocks
	}
	if err := DiscardUncommittedTails(root, committed); err != nil {
		return nil, err
	}
	if cp == nil {
		cp = &Checkpoint{InputFingerprint: inputFP, BlockSize: blockSize}
	}

	rep := repair.RepairManifest(m, 1000)
	result := &Result{
		InputFingerprint: inputFP,
		MetadataFP:       metadataFP,
		PackagingFP:      packagingFP,
		Repair:           rep,
		Resumed:          resumed,
		Checkpoint:       cp,
	}
	if st != nil {
		result.SampleCount = len(st.SampleOffsets)
	}

	numBlocks := (len(m.Frames) + blockSize - 1) / blockSize
	result.SegmentHashes = make([]string, numBlocks)
	var records []index.Record
	var pixels probe.PixelStats
	var blockHasher hash.Hash
	var blockWriter io.Writer
	var stageFile *os.File

	for i, fr := range m.Frames {
		blockIndex := i / blockSize
		contentFP := "content:" + fr.SHA256
		if opts.Cache != nil {
			opts.Cache.ObservePath(fr.Path, contentFP)
		}
		var img image.Image
		if opts.Cache != nil {
			if cached, ok := opts.Cache.GetDecode(contentFP); ok {
				img = cached
			}
		}
		if img == nil {
			decoded, _, err := source.DecodeFrame(root, fr.Path)
			if err != nil {
				return nil, err
			}
			img = decoded
			if opts.Cache != nil {
				opts.Cache.PutDecode(contentFP, img)
			}
		}
		pixels.Add(probe.ImagePixelStats(img))
		result.FrameHashes = append(result.FrameHashes, fr.SHA256)

		frameFull, err := fsx.SafeJoin(root, fr.Path)
		if err != nil {
			return nil, err
		}
		frameFile, err := os.Open(frameFull)
		if err != nil {
			return nil, media.E(media.CodeWrite, "segment.open_frame", err).WithFile(fr.Path).WithFrame(fr.Sequence)
		}

		// One hasher per block. New blocks are simultaneously staged to disk.
		if i%blockSize == 0 {
			blockHasher = sha256.New()
			blockWriter = io.Writer(blockHasher)
			stageFile = nil
			if blockIndex >= cp.CommittedBlocks {
				stageFile, err = os.Create(stagePath(root, blockIndex))
				if err != nil {
					_ = frameFile.Close()
					return nil, media.E(media.CodeWrite, "segment.stage_create", err).WithFrame(fr.Sequence)
				}
				blockWriter = io.MultiWriter(blockHasher, stageFile)
			}
		}
		if _, err := io.Copy(blockWriter, frameFile); err != nil {
			_ = frameFile.Close()
			closeStage(stageFile)
			return nil, media.E(media.CodeWrite, "segment.copy", err).WithFile(fr.Path).WithFrame(fr.Sequence)
		}
		if err := frameFile.Close(); err != nil {
			closeStage(stageFile)
			return nil, media.E(media.CodeWrite, "segment.close_frame", err).WithFile(fr.Path)
		}

		rec := index.Record{
			Sequence:       fr.Sequence,
			Track:          fr.Track,
			RepairedTimeUS: rep.Frames[i].RepairedTimeUS,
			RawTicks:       fr.RawTicks,
			FrameSHA256:    fr.SHA256,
			RelativePath:   fr.Path,
		}
		if st != nil && i < len(st.SampleOffsets) {
			rec.SampleOffset = st.SampleOffsets[i].Offset
			rec.SampleSize = st.SampleOffsets[i].Size
		}
		records = append(records, rec)

		blockEnd := (i+1)%blockSize == 0 || i == len(m.Frames)-1
		if !blockEnd {
			continue
		}
		segmentHash := hex.EncodeToString(blockHasher.Sum(nil))
		result.SegmentHashes[blockIndex] = segmentHash

		if blockIndex < cp.CommittedBlocks {
			if err := verifyCommittedSegment(root, blockIndex, segmentHash); err != nil {
				closeStage(stageFile)
				return nil, err
			}
		} else {
			if err := syncAndCloseStage(stageFile); err != nil {
				return nil, err
			}
			stageFile = nil
			if crashBefore == blockIndex {
				return nil, media.E(media.CodeInterrupt, "batch.crash_before_commit", nil).WithOffset(int64(blockIndex))
			}
			if err := fsx.CommitStage(stagePath(root, blockIndex), segmentPath(root, blockIndex)); err != nil {
				return nil, err
			}
			idx, err := index.Build(inputFP, records, blockSize)
			if err != nil {
				return nil, err
			}
			for b := range result.SegmentHashes {
				if b < len(idx.Blocks) && result.SegmentHashes[b] != "" {
					idx.Blocks[b].SegmentSHA256 = result.SegmentHashes[b]
				}
			}
			if err := idx.Save(indexPath(root)); err != nil {
				return nil, err
			}
			idxHash, err := idx.Hash()
			if err != nil {
				return nil, err
			}
			cp.CommittedBlocks = blockIndex + 1
			cp.ProcessedFrames = len(records)
			cp.IndexSHA256 = idxHash
			cp.Complete = cp.CommittedBlocks == numBlocks
			if err := SaveCheckpoint(root, cp); err != nil {
				return nil, err
			}
			if opts.InterruptAfterBlocks > 0 && cp.CommittedBlocks == opts.InterruptAfterBlocks {
				return result, media.E(media.CodeInterrupt, "batch.after_block", nil).WithOffset(int64(cp.CommittedBlocks))
			}
		}
		if stageFile != nil {
			_ = stageFile.Close()
			stageFile = nil
		}
	}

	idx, err := index.Load(indexPath(root))
	if err != nil {
		return nil, err
	}
	idxHash, err := idx.Hash()
	if err != nil {
		return nil, err
	}
	result.Index = idx
	result.IndexSHA256 = idxHash
	result.Pixels = pixels
	result.finalizeVisualMean()
	if m.Audio != nil {
		if err := readAndAlignAudio(root, m.Audio.Path, rep, blockSize, numBlocks, result); err != nil {
			return nil, err
		}
	}
	if opts.Cache != nil {
		result.CacheCounters = opts.Cache.Snapshot()
	}
	return result, nil
}
