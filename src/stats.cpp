#include "h264/stats.hpp"

namespace h264 {

namespace {

void count_type(PictureTypeCounts* counts, SliceKind kind) {
  switch (kind) {
    case SliceKind::kI: ++counts->i; break;
    case SliceKind::kP: ++counts->p; break;
    case SliceKind::kB: ++counts->b; break;
    case SliceKind::kSP: ++counts->sp; break;
    case SliceKind::kSI: ++counts->si; break;
  }
}

}  // namespace

StreamStats compute_stats(const std::vector<std::uint8_t>& data,
                          const std::vector<NalUnit>& nals,
                          const ParameterSets& sets,
                          const std::vector<Picture>& pictures,
                          const GopIndex& gops, bool incomplete) {
  StreamStats stats;
  stats.nal_count = static_cast<int>(nals.size());
  for (const NalUnit& nal : nals) {
    switch (nal.nal_unit_type) {
      case 7: ++stats.sps_count; break;
      case 8: ++stats.pps_count; break;
      case 6: ++stats.sei_count; break;
      default: break;
    }
  }

  // Re-read each picture's first slice header to obtain its type through the
  // same decoder path used for assembly.
  for (const Picture& pic : pictures) {
    const std::size_t offset = pic.slice_offsets.empty()
                                   ? pic.first_nal_offset
                                   : pic.slice_offsets.front();
    const NalUnit* nal = nullptr;
    for (const NalUnit& candidate : nals) {
      if (candidate.offset == offset) {
        nal = &candidate;
        break;
      }
    }
    if (nal == nullptr) continue;
    const SliceHeader header = parse_slice_header(
        data.data() + nal->payload_offset, nal->payload_size,
        nal->nal_unit_type, nal->ref_idc, nal->offset, sets);
    ++stats.pictures;
    if (header.field_pic_flag) {
      ++stats.field_pictures;
      count_type(&stats.field_types, header.slice_kind);
    } else {
      ++stats.frame_pictures;
      count_type(&stats.picture_types, header.slice_kind);
    }
    count_type(&stats.all_types, header.slice_kind);
  }

  for (const NalUnit& nal : nals) {
    if (is_vcl_type(nal.nal_unit_type)) ++stats.slices;
  }

  stats.has_idr = gops.has_idr;
  stats.gop_count = static_cast<int>(gops.gops.size());
  stats.ungrouped_picture_count =
      static_cast<int>(gops.ungrouped_prefix.size());
  if (!gops.gops.empty()) {
    stats.first_gop_offset = gops.gops.front().start_offset_bytes;
  }
  stats.incomplete = incomplete;
  return stats;
}

}  // namespace h264
