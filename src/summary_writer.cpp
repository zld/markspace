#include "summary_writer.hpp"

#include <iomanip>
#include <ostream>
#include <sstream>

namespace h264 {
namespace {

std::string json_escape(const std::string& in) {
    std::string out;
    out.reserve(in.size() + 2);
    for (char c : in) {
        const unsigned char u = static_cast<unsigned char>(c);
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\b': out += "\\b"; break;
            case '\f': out += "\\f"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (u < 0x20u) {
                    std::ostringstream oss;
                    oss << "\\u" << std::hex << std::setw(4)
                        << std::setfill('0') << static_cast<unsigned>(u);
                    out += oss.str();
                } else {
                    out += c;
                }
        }
    }
    return out;
}

double ratio(std::size_t part, std::size_t whole) {
    if (whole == 0) return 0.0;
    return 100.0 * static_cast<double>(part) / static_cast<double>(whole);
}

const char* bool_str(bool b) { return b ? "true" : "false"; }

}  // namespace

void write_text_summary(const ScanSummary& s, std::ostream& out) {
    out << "H.264 bitstream summary\n";
    out << "  input: " << s.input_path << "\n";
    out << "  file_size_bytes: " << s.file_size << "\n";
    out << "  nal_count: " << s.nal_count << "\n";
    out << "  max_nal_bytes: " << s.max_nal_bytes << "\n";

    out << "  parameter sets:\n";
    for (const auto& kv : s.sps_map) {
        const SpsInfo& sps = kv.second;
        out << "    SPS[" << sps.sps_id << "] profile=" << sps.profile_idc
            << " level=" << sps.level_idc
            << " chroma=" << sps.chroma_format_idc
            << " luma_bitdepth=" << sps.bit_depth_luma_minus8 + 8
            << " " << sps.width << "x" << sps.height
            << " frame_mbs_only=" << sps.frame_mbs_only_flag << "\n";
    }
    for (const auto& kv : s.pps_map) {
        const PpsInfo& pps = kv.second;
        out << "    PPS[" << pps.pps_id << "] sps=" << pps.sps_id
            << " cabac=" << pps.entropy_coding_mode_flag << "\n";
    }

    const std::size_t total = s.frames.total();
    out << "  frames: total=" << total
        << " I=" << s.frames.i << " P=" << s.frames.p
        << " B=" << s.frames.b << " unknown=" << s.frames.unknown << "\n";
    out << "  frame_pictures=" << s.frames.frame_pics
        << " field_pictures=" << s.frames.field_pics << "\n";
    out << "  ratios: I=" << std::fixed << std::setprecision(2)
        << ratio(s.frames.i, total) << "% P=" << ratio(s.frames.p, total)
        << "% B=" << ratio(s.frames.b, total) << "%\n";

    out << "  leading_pictures_without_idr: " << s.leading.size() << "\n";
    out << "  groups: " << s.groups.size() << "\n";
    for (std::size_t i = 0; i < s.groups.size(); ++i) {
        const GopInfo& g = s.groups[i];
        out << "    gop[" << i << "] start_offset=" << g.start_offset
            << " pictures=" << g.picture_count
            << " closed=" << bool_str(g.closed) << "\n";
    }

    if (s.diagnostics != nullptr) {
        out << "  errors: " << s.diagnostics->size() << "\n";
        for (const Diag& d : *s.diagnostics) {
            out << "    @" << d.offset << " " << d.message << "\n";
        }
    }
}

void write_json_summary(const ScanSummary& s, std::ostream& out) {
    const std::size_t total = s.frames.total();
    out << "{\n";
    out << "  \"input\": \"" << json_escape(s.input_path) << "\",\n";
    out << "  \"file_size_bytes\": " << s.file_size << ",\n";
    out << "  \"max_nal_bytes\": " << s.max_nal_bytes << ",\n";
    out << "  \"nal_count\": " << s.nal_count << ",\n";

    out << "  \"sps\": [";
    bool first = true;
    for (const auto& kv : s.sps_map) {
        const SpsInfo& sps = kv.second;
        if (!first) out << ",";
        first = false;
        out << "\n    {\"sps_id\": " << sps.sps_id
            << ", \"profile_idc\": " << sps.profile_idc
            << ", \"constraint_flags\": " << sps.constraint_flags
            << ", \"level_idc\": " << sps.level_idc
            << ", \"chroma_format_idc\": " << sps.chroma_format_idc
            << ", \"bit_depth_luma\": " << sps.bit_depth_luma_minus8 + 8
            << ", \"bit_depth_chroma\": " << sps.bit_depth_chroma_minus8 + 8
            << ", \"width\": " << sps.width
            << ", \"height\": " << sps.height
            << ", \"frame_mbs_only_flag\": " << bool_str(sps.frame_mbs_only_flag)
            << ", \"mb_adaptive_frame_field_flag\": "
            << bool_str(sps.mb_adaptive_frame_field_flag)
            << "}";
    }
    out << (first ? "" : "\n  ") << "],\n";

    out << "  \"pps\": [";
    first = true;
    for (const auto& kv : s.pps_map) {
        const PpsInfo& pps = kv.second;
        if (!first) out << ",";
        first = false;
        out << "\n    {\"pps_id\": " << pps.pps_id
            << ", \"sps_id\": " << pps.sps_id
            << ", \"entropy_coding_mode_flag\": "
            << bool_str(pps.entropy_coding_mode_flag) << "}";
    }
    out << (first ? "" : "\n  ") << "],\n";

    out << "  \"frames\": {"
        << "\"I\": " << s.frames.i
        << ", \"P\": " << s.frames.p
        << ", \"B\": " << s.frames.b
        << ", \"unknown\": " << s.frames.unknown
        << ", \"total\": " << total
        << ", \"frame_pictures\": " << s.frames.frame_pics
        << ", \"field_pictures\": " << s.frames.field_pics
        << "},\n";

    out << "  \"ratios_percent\": {"
        << "\"I\": " << std::fixed << std::setprecision(2)
        << ratio(s.frames.i, total)
        << ", \"P\": " << ratio(s.frames.p, total)
        << ", \"B\": " << ratio(s.frames.b, total) << "},\n";

    out << "  \"leading_pictures\": " << s.leading.size() << ",\n";

    out << "  \"groups\": {\"count\": " << s.groups.size()
        << ", \"items\": [";
    for (std::size_t i = 0; i < s.groups.size(); ++i) {
        const GopInfo& g = s.groups[i];
        if (i != 0) out << ",";
        out << "\n    {\"index\": " << i
            << ", \"start_offset\": " << g.start_offset
            << ", \"first_picture\": " << g.first_picture
            << ", \"picture_count\": " << g.picture_count
            << ", \"closed\": " << bool_str(g.closed) << "}";
    }
    out << (s.groups.empty() ? "" : "\n  ") << "]},\n";

    out << "  \"nals\": [";
    for (std::size_t i = 0; i < s.nals.size(); ++i) {
        const NalMeta& n = s.nals[i];
        if (i != 0) out << ",";
        out << "\n    {\"start_offset\": " << n.start_offset
            << ", \"nal_offset\": " << n.nal_offset
            << ", \"raw_size\": " << n.raw_size
            << ", \"nal_ref_idc\": " << n.nal_ref_idc
            << ", \"nal_type\": " << n.nal_type
            << ", \"type_name\": \"" << nal_type_name(n.nal_type) << "\""
            << ", \"valid\": " << bool_str(n.valid) << "}";
    }
    out << (s.nals.empty() ? "" : "\n  ") << "],\n";

    out << "  \"errors\": [";
    if (s.diagnostics != nullptr) {
        for (std::size_t i = 0; i < s.diagnostics->size(); ++i) {
            const Diag& d = (*s.diagnostics)[i];
            if (i != 0) out << ",";
            out << "\n    {\"offset\": " << d.offset
                << ", \"message\": \"" << json_escape(d.message) << "\"}";
        }
        if (s.diagnostics->size() != 0u) out << "\n  ";
    }
    out << "]\n";
    out << "}\n";
}

}  // namespace h264
