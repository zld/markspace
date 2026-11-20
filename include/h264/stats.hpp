#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

#include "h264/au_assembler.hpp"
#include "h264/gop_index.hpp"
#include "h264/nal.hpp"
#include "h264/syntax.hpp"

namespace h264 {

struct PictureTypeCounts {
  int i = 0;
  int p = 0;
  int b = 0;
  int sp = 0;
  int si = 0;
  int total() const { return i + p + b + sp + si; }
};

struct StreamStats {
  int nal_count = 0;
  int sps_count = 0;
  int pps_count = 0;
  int sei_count = 0;

  int pictures = 0;
  int slices = 0;  // total VCL NAL units, one per slice
  int field_pictures = 0;
  int frame_pictures = 0;
  PictureTypeCounts picture_types;  // frame pictures only
  PictureTypeCounts field_types;    // field pictures only
  PictureTypeCounts all_types;      // every coded picture

  bool incomplete = false;
  bool has_idr = false;
  int gop_count = 0;
  int ungrouped_picture_count = 0;
  std::size_t first_gop_offset = 0;
};

// Re-parses every VCL NAL header with the shared Exp-Golomb decoder and
// aggregates picture types per assembled picture (never per VCL NAL).
StreamStats compute_stats(const std::vector<std::uint8_t>& data,
                          const std::vector<NalUnit>& nals,
                          const ParameterSets& sets,
                          const std::vector<Picture>& pictures,
                          const GopIndex& gops, bool incomplete);

}  // namespace h264
