#include "report.hpp"

#include <cstdio>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <ostream>
#include <sstream>

#include "media_types.hpp"

namespace pwsync {

namespace {

std::string escapeJson(const std::string& in) {
    std::string out;
    out.reserve(in.size() + 2);
    for (char c : in) {
        switch (c) {
            case '"':
                out += "\\\"";
                break;
            case '\\':
                out += "\\\\";
                break;
            case '\n':
                out += "\\n";
                break;
            case '\r':
                out += "\\r";
                break;
            case '\t':
                out += "\\t";
                break;
            default:
                if (static_cast<unsigned char>(c) < 0x20) {
                    char buf[8];
                    std::snprintf(buf, sizeof(buf), "\\u%04x",
                                  static_cast<unsigned char>(c));
                    out += buf;
                } else {
                    out += c;
                }
        }
    }
    return out;
}

// Fixed-precision doubles keep output byte-stable across runs and platforms.
std::string num(double value) {
    std::ostringstream ss;
    ss << std::fixed << std::setprecision(3) << value;
    return ss.str();
}

void appendOffsetStats(std::ostream& out, const OffsetStats& s,
                       const std::string& indent) {
    out << indent << "\"samples\": " << s.samples << ",\n";
    out << indent << "\"p50_ms\": " << num(s.p50_ms) << ",\n";
    out << indent << "\"p95_ms\": " << num(s.p95_ms) << ",\n";
    out << indent << "\"max_abs_ms\": " << num(s.max_abs_ms) << ",\n";
    out << indent << "\"mean_ms\": " << num(s.mean_ms) << ",\n";
    out << indent << "\"histogram_edges_ms\": [";
    for (std::size_t i = 0; i < kHistogramEdgesMs.size(); ++i) {
        if (i) {
            out << ", ";
        }
        out << num(kHistogramEdgesMs[i]);
    }
    out << "],\n";
    out << indent << "\"histogram_counts\": [";
    for (std::size_t i = 0; i < s.histogram.size(); ++i) {
        if (i) {
            out << ", ";
        }
        out << s.histogram[i];
    }
    out << "]\n";
}

void appendPath(std::ostream& out, const PathStats& p, const std::string& prefix) {
    out << "  \"" << prefix << "_delay_ms\": " << num(p.delay_ms) << ",\n";
    out << "  \"" << prefix << "_video_frames\": " << p.video_frames << ",\n";
    out << "  \"" << prefix << "_audio_frames\": " << p.audio_frames << ",\n";
    out << "  \"" << prefix << "_dropped_frames\": " << p.dropped_frames << ",\n";
    out << "  \"" << prefix << "_repeated_frames\": " << p.repeated_frames << ",\n";
    out << "  \"" << prefix << "_waits\": " << p.waits << ",\n";
    out << "  \"" << prefix << "_av_offset_ms\": {\n";
    appendOffsetStats(out, p.av_offset, "    ");
    out << "  }";
}

}  // namespace

std::string renderJsonString(const SyncReport& r) {
    std::ostringstream out;
    out << "{\n";
    out << "  \"input_file\": \"" << escapeJson(r.input_file) << "\",\n";
    out << "  \"clock_mode\": \"" << escapeJson(r.clock_mode) << "\",\n";
    out << "  \"late_threshold_ms\": " << num(r.late_threshold_ms) << ",\n";
    out << "  \"discontinuity_tolerance_ms\": "
        << num(r.discontinuity_tolerance_ms) << ",\n";
    out << "  \"media_duration_seconds\": " << num(r.media_duration_seconds) << ",\n";

    out << "  \"master_drift_ms\": {\n";
    out << "    \"mode\": \"" << escapeJson(r.clock.mode) << "\",\n";
    out << "    \"samples\": " << r.clock.samples << ",\n";
    out << "    \"resets\": " << r.clock.resets << ",\n";
    out << "    \"cumulative_drift_ms\": " << num(r.clock.cumulative_drift_ms) << ",\n";
    out << "    \"mean_drift_ms\": " << num(r.clock.mean_drift_ms) << ",\n";
    out << "    \"max_abs_drift_ms\": " << num(r.clock.max_abs_drift_ms) << "\n";
    out << "  },\n";

    appendPath(out, r.path_a, "path_a");
    out << ",\n";
    appendPath(out, r.path_b, "path_b");
    out << ",\n";

    out << "  \"timestamp_repairs\": " << r.timestamp_repairs << ",\n";
    out << "  \"discontinuities\": " << r.discontinuities << ",\n";
    out << "  \"corrupted_packets\": " << r.corrupted_packets << ",\n";
    out << "  \"decode_failures\": " << r.decode_failures << ",\n";
    out << "  \"video_packets\": " << r.video_packets << ",\n";
    out << "  \"audio_packets\": " << r.audio_packets << ",\n";
    out << "  \"video_frames_decoded\": " << r.video_frames_decoded << ",\n";
    out << "  \"audio_frames_decoded\": " << r.audio_frames_decoded << ",\n";
    out << "  \"flushed_frames\": " << r.flushed_frames << ",\n";

    out << "  \"errors\": [";
    for (std::size_t i = 0; i < r.errors.size(); ++i) {
        const auto& e = r.errors[i];
        out << (i ? ",\n    " : "\n    ");
        out << "{\"category\": \"" << escapeJson(toString(e.category))
            << "\", \"stream_index\": " << e.stream_index
            << ", \"message\": \"" << escapeJson(e.message) << "\"}";
    }
    out << (r.errors.empty() ? "" : "\n  ");
    out << "]\n";
    out << "}\n";
    return out.str();
}

bool writeJsonReport(const SyncReport& report, std::ostream& out) {
    out << renderJsonString(report);
    return static_cast<bool>(out);
}

bool writeJsonReportToFile(const SyncReport& report, const std::string& path) {
    if (path == "-") {
        return writeJsonReport(report, std::cout);
    }
    std::ofstream file(path, std::ios::binary | std::ios::trunc);
    if (!file) {
        return false;
    }
    const bool ok = writeJsonReport(report, file);
    file.close();
    return ok && file;
}

void writeTextSummary(const SyncReport& r, std::ostream& out) {
    out << "pw_sync report\n";
    out << "  input:                 " << r.input_file << "\n";
    out << "  clock mode:            " << r.clock_mode << "\n";
    out << "  media duration (s):    " << num(r.media_duration_seconds) << "\n";
    out << "  master drift cum (ms): " << num(r.clock.cumulative_drift_ms) << "\n";
    out << "  master drift max (ms): " << num(r.clock.max_abs_drift_ms) << "\n";
    out << "  clock resets:          " << r.clock.resets << "\n";
    const PathStats* paths[2] = {&r.path_a, &r.path_b};
    for (const auto* p : paths) {
        out << "  " << p->name << ":\n";
        out << "    delay ms:           " << num(p->delay_ms) << "\n";
        out << "    av offset P50/P95/maxabs (ms): "
            << num(p->av_offset.p50_ms) << " / " << num(p->av_offset.p95_ms)
            << " / " << num(p->av_offset.max_abs_ms) << "\n";
        out << "    dropped/repeated:   " << p->dropped_frames << " / "
            << p->repeated_frames << "\n";
    }
    out << "  timestamp repairs:     " << r.timestamp_repairs << "\n";
    out << "  discontinuities:       " << r.discontinuities << "\n";
    out << "  flushed frames:        " << r.flushed_frames << "\n";
    out << "  errors:                " << r.errors.size() << "\n";
    for (const auto& e : r.errors) {
        out << "    [" << toString(e.category) << " stream=" << e.stream_index
            << "] " << e.message << "\n";
    }
}

}  // namespace pwsync
