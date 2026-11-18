#include "compatibility.hpp"

#include <algorithm>
#include <map>
#include <set>

namespace mp4 {

const char* config_issue_name(ConfigIssue::Category c) {
    switch (c) {
        case ConfigIssue::MissingTrack: return "missing_track";
        case ConfigIssue::ExtraTrack: return "extra_track";
        case ConfigIssue::TimescaleDrift: return "timescale_drift";
        case ConfigIssue::CodecConfigDrift: return "codec_config_drift";
        case ConfigIssue::DefaultDurationDrift:
            return "default_sample_duration_drift";
        case ConfigIssue::DefaultSizeDrift: return "default_sample_size_drift";
        case ConfigIssue::DefaultFlagsDrift:
            return "default_sample_flags_drift";
    }
    return "unknown";
}

std::vector<uint32_t> present_sequences(const MediaIndex& idx) {
    std::vector<uint32_t> out;
    out.reserve(idx.fragments.size());
    for (const Fragment& f : idx.fragments)
        out.push_back(f.sequence_number);
    return out;
}

std::vector<MissingFragment> find_missing_fragments(
    const std::vector<uint32_t>& present) {
    std::vector<MissingFragment> out;
    if (present.empty()) return out;
    std::set<uint32_t> unique(present.begin(), present.end());
    uint32_t lo = *unique.begin();
    uint32_t hi = *unique.rbegin();
    for (uint32_t seq = lo; seq < hi; ++seq) {  // overflow-safe: seq < hi
        if (!unique.count(seq)) {
            MissingFragment m;
            m.expected_sequence = seq;
            auto it = unique.lower_bound(seq);
            m.next_actual = (it != unique.end()) ? *it : 0;
            if (it != unique.begin())
                m.previous_actual = *std::prev(it);
            out.push_back(m);
        }
    }
    return out;
}

namespace {

void check_config(const MediaIndex& idx, CompatReport& r) {
    std::set<uint32_t> seen;
    for (const Fragment& f : idx.fragments)
        for (const TrackFragment& tf : f.tracks) {
            seen.insert(tf.track_id);
            const TrackConfig& cfg = idx.tracks.at(tf.track_id);

            if (tf.tfhd_flags & 0x000002 /*default-duration-present*/ &&
                tf.default_sample_duration != cfg.default_sample_duration) {
                r.config_issues.push_back(
                    {ConfigIssue::DefaultDurationDrift, tf.track_id,
                     "fragment default_sample_duration " +
                         std::to_string(tf.default_sample_duration) +
                         " != init " +
                         std::to_string(cfg.default_sample_duration)});
            }
            if (tf.tfhd_flags & 0x000008 /*default-size-present*/ &&
                tf.default_sample_size != cfg.default_sample_size) {
                r.config_issues.push_back(
                    {ConfigIssue::DefaultSizeDrift, tf.track_id,
                     "fragment default_sample_size " +
                         std::to_string(tf.default_sample_size) +
                         " != init " +
                         std::to_string(cfg.default_sample_size)});
            }
            if (tf.tfhd_flags & 0x000020 /*default-flags-present*/ &&
                tf.default_sample_flags != cfg.default_sample_flags) {
                r.config_issues.push_back(
                    {ConfigIssue::DefaultFlagsDrift, tf.track_id,
                     "fragment default_sample_flags 0x" +
                         std::to_string(tf.default_sample_flags) +
                         " != init 0x" +
                         std::to_string(cfg.default_sample_flags)});
            }
        }

    for (const auto& [id, cfg] : idx.tracks)
        if (!seen.count(id))
            r.config_issues.push_back(
                {ConfigIssue::MissingTrack, id,
                 "track " + std::to_string(id) +
                     " declared in moov never appears in a fragment"});
}

void check_sequences(const MediaIndex& idx, CompatReport& r) {
    std::map<uint32_t, size_t> first_index;
    uint32_t running_max = 0;
    for (size_t i = 0; i < idx.fragments.size(); ++i) {
        uint32_t seq = idx.fragments[i].sequence_number;
        auto it = first_index.find(seq);
        if (it != first_index.end())
            r.sequence_issues.push_back(
                {SequenceIssue::Duplicate,
                 idx.fragments[it->second].sequence_number, seq});
        else
            first_index.emplace(seq, i);
        if (i > 0 && seq < running_max)
            r.sequence_issues.push_back(
                {SequenceIssue::OutOfOrder, running_max, seq});
        running_max = std::max(running_max, seq);
    }

    std::vector<uint32_t> present = present_sequences(idx);
    r.missing_fragments = find_missing_fragments(present);
}

void check_timelines(const MediaIndex& idx, CompatReport& r) {
    for (const auto& [id, tl] : idx.timelines) {
        TrackDeviation dev;
        dev.track_id = id;
        dev.fragment_count = 0;
        for (const Fragment& f : idx.fragments)
            for (const TrackFragment& tf : f.tracks)
                if (tf.track_id == id) ++dev.fragment_count;
        uint64_t worst = 0;
        for (const TimelineIssue& issue : tl.issues) {
            worst = std::max(worst, issue.delta());
            r.warnings.push_back(
                std::string("track ") + std::to_string(id) + " " +
                (issue.kind == TimelineIssue::Gap ? "gap" : "overlap") +
                " at sequence " + std::to_string(issue.sequence) +
                " (expected dts " + std::to_string(issue.expected) +
                ", got " + std::to_string(issue.actual) + ", delta " +
                std::to_string(issue.delta()) + " ticks)");
        }
        dev.max_abs_deviation_ticks = worst;
        r.track_deviations.push_back(dev);
    }
    std::sort(r.track_deviations.begin(), r.track_deviations.end(),
              [](const TrackDeviation& a, const TrackDeviation& b) {
                  return a.track_id < b.track_id;
              });
}

}  // namespace

CompatReport check_compatibility(const MediaIndex& idx) {
    CompatReport r;
    if (idx.fragments.empty()) {
        r.ok = false;
        r.errors.push_back("no fragments to validate");
        return r;
    }
    check_config(idx, r);
    check_sequences(idx, r);
    check_timelines(idx, r);

    for (const ConfigIssue& c : r.config_issues)
        r.errors.push_back(std::string("[") + config_issue_name(c.category) +
                           "] " + c.detail);
    for (const SequenceIssue& s : r.sequence_issues) {
        if (s.category == SequenceIssue::Duplicate)
            r.errors.push_back("[duplicate_sequence] sequence " +
                               std::to_string(s.repeated_occurrence));
        else
            r.warnings.push_back("[out_of_order_sequence] sequence " +
                                 std::to_string(s.repeated_occurrence) +
                                 " arrived after " +
                                 std::to_string(s.first_occurrence));
    }
    for (const MissingFragment& m : r.missing_fragments)
        r.warnings.push_back(
            "[missing_fragment] expected sequence " +
            std::to_string(m.expected_sequence) + " (previous actual " +
            std::to_string(m.previous_actual) + ", next actual " +
            std::to_string(m.next_actual) + ")");
    r.ok = r.errors.empty();
    return r;
}

}  // namespace mp4
