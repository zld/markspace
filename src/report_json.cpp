#include "report.hpp"

#include <sstream>

namespace h264 {
namespace {

std::string json_escape(const std::string& in) {
    std::string out;
    out.reserve(in.size() + 2);
    for (char c : in) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
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

const char* type_json(PictureType t) {
    switch (t) {
        case PictureType::kI: return "\"I\"";
        case PictureType::kP: return "\"P\"";
        case PictureType::kB: return "\"B\"";
        default: return "\"unknown\"";
    }
}

}  // namespace

std::string render_json(const StreamSummary& s) {
    std::ostringstream o;
    o << "{\n";
    o << "  \"sps\": {\n";
    o << "    \"present\": " << (s.sps.present ? "true" : "false") << ",\n";
    o << "    \"valid\": " << (s.sps.valid ? "true" : "false") << ",\n";
    o << "    \"profile_idc\": " << s.sps.profile_idc << ",\n";
    o << "    \"level_idc\": " << s.sps.level_idc << ",\n";
    o << "    \"seq_parameter_set_id\": " << s.sps.seq_parameter_set_id
      << ",\n";
    o << "    \"width\": " << s.sps.width << ",\n";
    o << "    \"height\": " << s.sps.height << ",\n";
    o << "    \"frame_mbs_only\": "
      << (s.sps.frame_mbs_only_flag ? "true" : "false") << ",\n";
    o << "    \"crop_left\": " << s.sps.crop_left
      << ", \"crop_right\": " << s.sps.crop_right
      << ", \"crop_top\": " << s.sps.crop_top
      << ", \"crop_bottom\": " << s.sps.crop_bottom << "\n";
    o << "  },\n";

    std::size_t i = 0, p = 0, b = 0;
    o << "  \"images\": [\n";
    for (std::size_t k = 0; k < s.images.size(); ++k) {
        const ImageRecord& im = s.images[k];
        if (im.picture_type == PictureType::kI) ++i;
        else if (im.picture_type == PictureType::kP) ++p;
        else if (im.picture_type == PictureType::kB) ++b;
        o << "    {\"index\": " << k
          << ", \"type\": " << type_json(im.picture_type)
          << ", \"frame_num\": " << im.frame_num
          << ", \"field_pic\": " << (im.field_pic_flag ? "true" : "false")
          << ", \"bottom_field\": "
          << (im.bottom_field_flag ? "true" : "false")
          << ", \"idr\": " << (im.idr ? "true" : "false")
          << ", \"start_offset_bytes\": " << im.start_offset_bytes
          << ", \"slice_count\": " << im.slice_nal_offsets.size()
          << ", \"unknown_slice_count\": " << im.unknown_slice_count << "}";
        if (k + 1 < s.images.size()) o << ",";
        o << "\n";
    }
    o << "  ],\n";

    o << "  \"groups\": [\n";
    for (std::size_t k = 0; k < s.groups.size(); ++k) {
        const PictureGroup& g = s.groups[k];
        o << "    {\"index\": " << k
          << ", \"start_offset_bytes\": " << g.start_offset_bytes
          << ", \"image_count\": " << g.image_count
          << ", \"i_count\": " << g.i_count
          << ", \"p_count\": " << g.p_count
          << ", \"b_count\": " << g.b_count << "}";
        if (k + 1 < s.groups.size()) o << ",";
        o << "\n";
    }
    o << "  ],\n";

    o << "  \"summary\": {\n";
    o << "    \"image_count\": " << s.images.size() << ",\n";
    o << "    \"i_count\": " << i << ",\n";
    o << "    \"p_count\": " << p << ",\n";
    o << "    \"b_count\": " << b << ",\n";
    o << "    \"group_count\": " << s.groups.size() << ",\n";
    o << "    \"unknown_slice_count\": " << s.unknown_slice_count << ",\n";
    o << "    \"unsupported_nal_count\": " << s.unsupported_nal_count << "\n";
    o << "  },\n";

    o << "  \"diagnostics\": [\n";
    const auto& items = s.diagnostics.items();
    for (std::size_t k = 0; k < items.size(); ++k) {
        const Diagnostic& d = items[k];
        o << "    {\"offset\": " << d.offset
          << ", \"severity\": \"" << json_escape(d.severity)
          << "\", \"code\": \"" << json_escape(d.code)
          << "\", \"message\": \"" << json_escape(d.message) << "\"}";
        if (k + 1 < items.size()) o << ",";
        o << "\n";
    }
    o << "  ],\n";
    o << "  \"diagnostic_counts\": {\"errors\": "
      << s.diagnostics.error_count()
      << ", \"warnings\": " << s.diagnostics.warning_count()
      << ", \"dropped\": " << s.diagnostics.dropped() << "}\n";
    o << "}\n";
    return o.str();
}

}  // namespace h264
