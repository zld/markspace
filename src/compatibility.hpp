#pragma once

#include "fragment_index.hpp"

#include <cstdint>
#include <string>
#include <vector>

namespace mp4 {

struct ConfigIssue {
    enum Category {
        MissingTrack,
        ExtraTrack,
        TimescaleDrift,
        CodecConfigDrift,
        DefaultDurationDrift,
        DefaultSizeDrift,
        DefaultFlagsDrift
    };
    Category category;
    uint32_t track_id = 0;
    std::string detail;
};

struct SequenceIssue {
    enum Category { Duplicate, OutOfOrder };
    Category category;
    uint32_t first_occurrence = 0;
    uint32_t repeated_occurrence = 0;
};

struct MissingFragment {
    uint32_t expected_sequence = 0;
    uint32_t previous_actual = 0;
    uint32_t next_actual = 0;  // 0 when the gap runs to the end
};

struct TrackDeviation {
    uint32_t track_id = 0;
    uint64_t max_abs_deviation_ticks = 0;
    uint64_t fragment_count = 0;
};

struct CompatReport {
    bool ok = true;
    std::vector<ConfigIssue> config_issues;
    std::vector<SequenceIssue> sequence_issues;
    std::vector<MissingFragment> missing_fragments;
    std::vector<TrackDeviation> track_deviations;
    // Soft problems: reported in the summary but repacking can continue.
    std::vector<std::string> warnings;
    // Hard failures: block writing (config conflicts, duplicates, structure).
    std::vector<std::string> errors;
};

// Compares fragment contents against the initialization segment configuration
// and checks sequence ordering/continuity and per-track decode timelines.
// The index is the exact same structure the tests and the rewriter consume.
CompatReport check_compatibility(const MediaIndex& idx);

// Sorted unique moof sequence numbers actually present in the input.
std::vector<uint32_t> present_sequences(const MediaIndex& idx);

// Missing sequence numbers between the observed minimum and maximum.
std::vector<MissingFragment> find_missing_fragments(
    const std::vector<uint32_t>& present);

const char* config_issue_name(ConfigIssue::Category c);

}  // namespace mp4
