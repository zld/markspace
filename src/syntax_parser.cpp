#include "h264/syntax.hpp"

#include <cstdio>

namespace h264 {

const char* slice_kind_name(SliceKind kind) {
  switch (kind) {
    case SliceKind::kP: return "P";
    case SliceKind::kB: return "B";
    case SliceKind::kI: return "I";
    case SliceKind::kSP: return "SP";
    case SliceKind::kSI: return "SI";
  }
  return "?";
}

int Sps::chroma_array_type() const {
  return separate_colour_plane_flag ? 0 : chroma_format_idc;
}


int Sps::width_pixels() const {
  const int width = pic_width_in_mbs * 16;
  if (!frame_cropping_flag) return width;
  const int chroma = chroma_array_type();
  const int crop_unit_x = (chroma == 1 || chroma == 2) ? 2 : 1;
  return width -
         crop_unit_x * (frame_crop_left_offset + frame_crop_right_offset);
}

int Sps::height_pixels() const {
  const int map_units = 2 - (frame_mbs_only_flag ? 1 : 0);
  int height = pic_height_in_map_units * map_units * 16;
  if (!frame_cropping_flag) return height;
  const int chroma = chroma_array_type();
  const int crop_unit_y =
      (chroma == 1 || chroma == 2) ? 2 * map_units : map_units;
  return height -
         crop_unit_y * (frame_crop_top_offset + frame_crop_bottom_offset);
}

const Sps& ParameterSets::require_sps(int id, std::size_t nal_offset) const {
  const auto it = sps.find(id);
  if (it == sps.end()) {
    fail("reference to unknown SPS id " + std::to_string(id), nal_offset, 0,
         true);
  }
  return it->second;
}

const Pps& ParameterSets::require_pps(int id, std::size_t nal_offset) const {
  const auto it = pps.find(id);
  if (it == pps.end()) {
    fail("reference to unknown PPS id " + std::to_string(id), nal_offset, 0,
         true);
  }
  return it->second;
}

}  // namespace h264
