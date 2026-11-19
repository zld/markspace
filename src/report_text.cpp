#include "report.hpp"

#include <sstream>

namespace h264 {

std::string render_text(const StreamSummary& s) {
    std::ostringstream o;
    o << "H.264 Annex B stream analysis\n";
    o << "=============================\n";
    o << "SPS:\n";
    if (!s.sps.present) {
        o << "  (no SPS found)\n";
    } else {
        o << "  profile_idc=" << s.sps.profile_idc
          << " level_idc=" << s.sps.level_idc
          << " sps_id=" << s.sps.seq_parameter_set_id
          << (s.sps.valid ? "" : " [INVALID]") << "\n";
        o << "  width=" << s.sps.width << " height=" << s.sps.height
          << " frame_mbs_only="
          << (s.sps.frame_mbs_only_flag ? "true" : "false") << "\n";
        if (s.sps.crop_left || s.sps.crop_right || s.sps.crop_top ||
            s.sps.crop_bottom) {
            o << "  crop l/r/t/b=" << s.sps.crop_left << "/"
              << s.sps.crop_right << "/" << s.sps.crop_top << "/"
              << s.sps.crop_bottom << "\n";
        }
    }

    std::size_t ic = 0, pc = 0, bc = 0;
    o << "\nImages (" << s.images.size() << "):\n";
    for (std::size_t k = 0; k < s.images.size(); ++k) {
        const ImageRecord& im = s.images[k];
        if (im.picture_type == PictureType::kI) ++ic;
        else if (im.picture_type == PictureType::kP) ++pc;
        else if (im.picture_type == PictureType::kB) ++bc;
        o << "  [" << k << "] " << picture_type_name(im.picture_type)
          << " frame_num=" << im.frame_num
          << " offset=" << im.start_offset_bytes
          << " slices=" << im.slice_nal_offsets.size();
        if (im.field_pic_flag) {
            o << " field=" << (im.bottom_field_flag ? "bottom" : "top");
        }
        if (im.idr) o << " IDR";
        if (im.unknown_slice_count) {
            o << " unknown_slices=" << im.unknown_slice_count;
        }
        o << "\n";
    }

    o << "\nPicture groups (" << s.groups.size() << "):\n";
    for (std::size_t k = 0; k < s.groups.size(); ++k) {
        const PictureGroup& g = s.groups[k];
        o << "  group[" << k << "] offset=" << g.start_offset_bytes
          << " images=" << g.image_count
          << " I=" << g.i_count << " P=" << g.p_count
          << " B=" << g.b_count << "\n";
    }

    o << "\nTotals: images=" << s.images.size()
      << " I=" << ic << " P=" << pc << " B=" << bc
      << " unknown_slices=" << s.unknown_slice_count
      << " unsupported_nals=" << s.unsupported_nal_count << "\n";

    o << "\nDiagnostics: " << s.diagnostics.error_count() << " error(s), "
      << s.diagnostics.warning_count() << " warning(s)";
    if (s.diagnostics.dropped()) {
        o << ", " << s.diagnostics.dropped() << " dropped";
    }
    o << "\n";
    for (const Diagnostic& d : s.diagnostics.items()) {
        o << "  " << d.severity << " @" << d.offset << " [" << d.code
          << "] " << d.message << "\n";
    }
    return o.str();
}

}  // namespace h264
