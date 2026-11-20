#include "h264/gop_index.hpp"

namespace h264 {

GopIndex build_gop_index(const std::vector<Picture>& pictures) {
  GopIndex index;
  std::size_t gop_begin = 0;
  bool in_gop = false;

  auto close_gop = [&](std::size_t end) {
    if (!in_gop) return;
    Gop gop;
    gop.index = index.gops.size();
    gop.picture_begin = gop_begin;
    gop.picture_end = end;
    gop.picture_count = static_cast<int>(end - gop_begin);
    gop.start_offset_bytes = pictures[gop_begin].first_nal_offset;
    gop.frame_num = pictures[gop_begin].frame_num;
    index.gops.push_back(gop);
  };

  for (std::size_t i = 0; i < pictures.size(); ++i) {
    const bool starts_gop = pictures[i].idr;
    if (starts_gop) {
      if (in_gop) close_gop(i);
      // A new GOP begins at the first picture of the IDR access unit.
      gop_begin = i;
      in_gop = true;
      index.has_idr = true;
    } else if (!in_gop) {
      index.ungrouped_prefix.push_back(i);
    }
  }
  close_gop(pictures.size());

  for (std::size_t i = 0; i < index.gops.size(); ++i) {
    index.gops[i].index = i;
  }
  return index;
}

}  // namespace h264
