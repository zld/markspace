#pragma once

#include <cstddef>
#include <string>
#include <vector>

#include "h264/au_assembler.hpp"

namespace h264 {

struct Gop {
  std::size_t index = 0;
  std::size_t start_offset_bytes = 0;  // first start code of the IDR picture
  std::size_t picture_begin = 0;       // index into the picture list
  std::size_t picture_end = 0;         // exclusive
  int picture_count = 0;
  int frame_num = 0;
};

struct GopIndex {
  std::vector<Gop> gops;
  // Pictures before the first IDR are listed here and are not part of a GOP.
  std::vector<std::size_t> ungrouped_prefix;
  bool has_idr = false;
};

GopIndex build_gop_index(const std::vector<Picture>& pictures);

}  // namespace h264
