package demo

import (
	"bytes"
	"path/filepath"

	"github.com/zld/medcheck/pkg/evidence"
	"github.com/zld/medcheck/pkg/index"
	"github.com/zld/medcheck/pkg/media"
)

func runAssetsAndCache(rep *evidence.Report, out string, seed int64, primary *analysis) error {
	reg, err := media.NewRegistry(filepath.Join(out, "asset-registry"))
	if err != nil {
		return err
	}
	sourcePath, err := media.SafeJoin(out, "same-path-source.h264")
	if err != nil {
		return err
	}
	logical := "cameras/cam-main/program.h264"
	v1 := []byte("immutable-source-revision-v1 seed=" + itoa(int(seed)))
	if err := media.WriteFileAtomic(sourcePath, bytes.NewReader(v1)); err != nil {
		return err
	}
	a1, err := reg.RegisterFile(logical, sourcePath, media.Fingerprint{})
	if err != nil {
		return err
	}
	v2 := append(append([]byte{}, v1...), []byte(" replacement-content-v2")...)
	if err := media.WriteFileAtomic(sourcePath, bytes.NewReader(v2)); err != nil {
		return err
	}
	a2, err := reg.RegisterFile(logical, sourcePath, media.Fingerprint{})
	if err != nil {
		return err
	}
	hist, err := reg.History(logical)
	if err != nil {
		return err
	}
	old, oldErr := reg.LoadAsset(a1.ID)
	oldRetained := oldErr == nil && old != nil && old.ID == a1.ID
	rep.Replacement = &evidence.ReplacementEvidence{
		LogicalPath: logical, OldAssetID: a1.ID, NewAssetID: a2.ID, OldRetained: oldRetained,
	}
	rep.Check("same_path_replacement_new_asset", a1.ID != a2.ID && hist.Current == a2.ID, "new digest", a2.ID, "old manifest is not overwritten")
	rep.Check("old_asset_retained", oldRetained && len(hist.AssetIDs) == 2, true, oldRetained, a1.ID)

	cache, err := media.NewCache(filepath.Join(out, "derivative-cache"))
	if err != nil {
		return err
	}
	streamD := media.SHA256Bytes(primary.fixture.Data).Value
	mp4A := BuildMP4Fixture(1, 90000, primary.fixture.Data, 0)
	mp4B := BuildMP4Fixture(1, 90000, primary.fixture.Data, 48)
	mp4C := WithMetadataTimescale(mp4A, 48000)
	if err := writeFixture(out, "container-a.mp4", mp4A); err != nil {
		return err
	}
	if err := writeFixture(out, "container-b-shifted.mp4", mp4B); err != nil {
		return err
	}
	if err := writeFixture(out, "container-metadata-changed.mp4", mp4C); err != nil {
		return err
	}

	_, mdA, err := media.ParseMP4Metadata(bytes.NewReader(mp4A))
	if err != nil {
		return err
	}
	_, mdB, err := media.ParseMP4Metadata(bytes.NewReader(mp4B))
	if err != nil {
		return err
	}
	metaC, mdC, err := media.ParseMP4Metadata(bytes.NewReader(mp4C))
	if err != nil {
		return err
	}
	containerA := media.SHA256Bytes(mp4A).Value
	containerB := media.SHA256Bytes(mp4B).Value
	fpA := media.Fingerprint{StreamSHA256: streamD, ContainerSHA256: containerA, MetadataSHA256: mdA}
	fpB := media.Fingerprint{StreamSHA256: streamD, ContainerSHA256: containerB, MetadataSHA256: mdB}
	fpC := media.Fingerprint{StreamSHA256: streamD, ContainerSHA256: containerA, MetadataSHA256: mdC}

	parseKey := cache.Key("keyframe-index", streamD)
	if err := cache.PutJSON(parseKey, primary.parsed); err != nil {
		return err
	}
	var cacheHit index.Index
	hit, err := cache.GetJSON(parseKey, &cacheHit)
	if err != nil {
		return err
	}
	containerState := media.ClassifyFingerprintChange(fpA, fpB)
	rep.Cache = append(rep.Cache, evidence.CacheEvidence{
		Scenario: "container-change", Expected: "stream_parse=hit,offset_mapping=invalid",
		State: containerState, StreamCache: map[bool]string{true: "hit", false: "miss"}[hit],
	})
	rep.Check("container_change_reuses_stream_cache", hit && containerState.StreamParse == "hit", "hit", containerState.StreamParse, "same elementary-stream digest")
	rep.Check("container_change_invalidates_offset_map", containerState.OffsetMapping == "invalid", "invalid", containerState.OffsetMapping, "container bytes shifted")

	offsetMap := index.BuildOffsetMapping(primary.parsed, streamD, containerA, 40)
	rawMap, err := offsetMap.Marshal()
	if err != nil {
		return err
	}
	if _, err := index.ParseOffsetMapping(rawMap); err != nil {
		return err
	}
	metaState := media.ClassifyFingerprintChange(fpA, fpC)
	metaKey := cache.Key("timeline-index", mdA)
	if err := cache.PutJSON(metaKey, map[string]any{"metadata": mdA}); err != nil {
		return err
	}
	var oldMeta any
	metaHit, err := cache.GetJSON(cache.Key("timeline-index", mdC), &oldMeta)
	if err != nil {
		return err
	}
	rep.Cache = append(rep.Cache, evidence.CacheEvidence{
		Scenario: "metadata-change timescale=" + itoa(int(metaC.Tracks[0].Timescale)),
		Expected: "stream_parse=hit,timeline_index=invalid", State: metaState, StreamCache: "hit",
	})
	rep.Check("metadata_change_keeps_stream_cache", metaState.StreamParse == "hit", "hit", metaState.StreamParse, streamD)
	rep.Check("metadata_change_invalidates_timeline_index", metaState.TimelineIndex == "invalid" && !metaHit, "invalid/miss", metaState.TimelineIndex+"/"+map[bool]string{true: "hit", false: "miss"}[metaHit], "normalized track metadata digest changed")
	return nil
}

func writeFixture(out, name string, b []byte) error {
	p, err := media.SafeJoin(out, name)
	if err != nil {
		return err
	}
	return media.WriteFileAtomic(p, bytes.NewReader(b))
}
