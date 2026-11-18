#pragma once

#include "box_reader.hpp"

#include <cstdint>
#include <map>
#include <memory>
#include <string>
#include <vector>

namespace mp4 {

// Configurable safety limits (task constraint: 2 GiB, samples/tracks tunable).
struct Limits {
    uint64_t max_file_size = 2ULL * 1024 * 1024 * 1024;
    uint64_t max_samples = 1'000'000;
    uint32_t max_tracks = 64;
};

struct IndexError : std::runtime_error {
    explicit IndexError(const std::string& m) : std::runtime_error(m) {}
};

// Track configuration extracted from the initialization segment (moov).
struct TrackConfig {
    uint32_t track_id = 0;
    uint32_t timescale = 0;
    uint64_t default_sample_duration = 0;  // from trex
    uint32_t default_sample_size = 0;      // from trex
    uint32_t default_sample_flags = 0;     // from trex
    std::vector<uint8_t> codec_config;     // raw stsd sample entries
    std::string codec_config_sha256;
};

struct SampleInfo {
    uint32_t size = 0;
    uint32_t duration = 0;
    int32_t composition_offset = 0;  // signed; negative allowed (trun v1)
    uint32_t flags = 0;
    uint64_t decode_time = 0;        // cumulative DTS on the track timeline
};

struct TimelineIssue {
    enum Kind { Gap, Overlap };
    Kind kind = Gap;
    uint32_t sequence = 0;
    uint64_t expected = 0;
    uint64_t actual = 0;
    uint64_t delta() const {
        return actual >= expected ? actual - expected : expected - actual;
    }
};

struct TrackTimeline {
    uint64_t next_decode_time = 0;  // expected tfdt of the next fragment
    uint64_t total_duration = 0;
    uint64_t sample_count = 0;
    std::vector<TimelineIssue> issues;
};

struct TrackFragment {
    uint32_t track_id = 0;
    const Box* traf = nullptr;
    uint32_t tfhd_flags = 0;
    uint64_t base_data_offset = 0;
    uint64_t default_sample_duration = 0;
    uint32_t default_sample_size = 0;
    uint32_t default_sample_flags = 0;
    uint8_t tfdt_version = 0;
    uint64_t base_media_decode_time = 0;
    uint8_t trun_version = 0;
    uint32_t trun_flags = 0;
    int32_t data_offset = 0;
    std::vector<SampleInfo> samples;
};

struct Fragment {
    std::string file;
    const struct MediaFile* source = nullptr;  // owning segment buffer
    const Box* moof = nullptr;
    uint32_t sequence_number = 0;
    std::vector<TrackFragment> tracks;
    uint64_t mdat_payload_offset = 0;  // absolute file offset of mdat data
    uint64_t mdat_payload_size = 0;
};

struct MediaIndex {
    std::map<uint32_t, TrackConfig> tracks;
    std::vector<Fragment> fragments;  // parse (file) order
    std::map<uint32_t, TrackTimeline> timelines;
};

// One loaded file: buffer and box tree must outlive any Box* in the index.
struct MediaFile {
    std::string path;
    std::vector<uint8_t> data;
    std::vector<Box> boxes;
    bool is_init = false;
};

struct MediaPackage {
    std::unique_ptr<MediaFile> init;
    std::vector<std::unique_ptr<MediaFile>> segments;
    MediaIndex index;
};

// --- initialization segment -------------------------------------------------

// Parses moov/trak/mdhd/trex/stsd into track configurations.
MediaIndex parse_init_segment(const std::vector<uint8_t>& data,
                              const Limits& lim = {});

// --- media segments ---------------------------------------------------------

// Parses every moof in one media file, appending fragments to the index and
// extending per-track decode timelines (gap/overlap detection, overflow checks).
void index_media_segment(MediaIndex& idx, const MediaFile& segment,
                         const Limits& lim = {});

// --- directory loading ------------------------------------------------------

struct ScanResult {
    std::string init_path;
    std::vector<std::string> segment_paths;  // sorted by file name
};

// Scans a directory for *.mp4/*.m4s/*.m4v/*.bin (non-recursive, sorted).
ScanResult scan_input_dir(const std::string& dir);

// Loads init + every media segment and builds the shared index.
MediaPackage load_package(const std::string& dir, const Limits& lim = {});

// Throws IndexError when a+b overflows uint64_t.
uint64_t checked_add(uint64_t a, uint64_t b);

}  // namespace mp4
