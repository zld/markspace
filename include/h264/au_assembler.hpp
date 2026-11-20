#pragma once

#include <cstddef>
#include <string>
#include <vector>

#include "h264/syntax.hpp"

namespace h264 {

// One coded picture. Multiple slices of the same picture collapse into a
// single entry; the top and bottom fields of an interlaced frame remain two
// distinct pictures, as required by H.264 picture accounting.
struct Picture {
  std::size_t first_nal_offset = 0;
  std::size_t last_nal_offset = 0;
  std::vector<std::size_t> slice_offsets;

  int frame_num = 0;
  int pps_id = 0;
  int sps_id = 0;
  int pic_order_cnt_lsb = 0;
  int idr_pic_id = 0;
  int nal_ref_idc = 0;
  bool idr = false;
  bool field_pic = false;
  bool bottom_field = false;
  SliceKind kind = SliceKind::kP;
  int slice_count = 0;
  bool complete = true;
};

struct AuAssembleResult {
  std::vector<Picture> pictures;
  // True when the stream ended or a damaged NAL left a picture unclosed and
  // the picture had to be dropped.
  bool incomplete = false;
  std::vector<std::string> warnings;
};

// Identity fields from 7.4.1.2.4 that distinguish two coded pictures.
bool same_picture_identity(const SliceHeader& a, const SliceHeader& b);

// Groups slice headers into pictures. Throws ParseError when two slices of the
// same picture disagree on slice_type.
//
// `damaged_after[i]` marks that a VCL NAL following slices[i] could not be
// parsed, so the picture containing slices[i] has an unknown slice count and
// must be dropped (recovery mode). An empty vector means no damage.
AuAssembleResult assemble_pictures(const std::vector<SliceHeader>& slices,
                                   const std::vector<bool>& damaged_after,
                                   bool stream_complete);

}  // namespace h264
