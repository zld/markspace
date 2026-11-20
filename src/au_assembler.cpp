#include "h264/au_assembler.hpp"

namespace h264 {

namespace {

bool same_field(const SliceHeader& a, const SliceHeader& b) {
  return a.field_pic_flag == b.field_pic_flag &&
         (!a.field_pic_flag || a.bottom_field_flag == b.bottom_field_flag);
}

void start_picture(Picture* pic, const SliceHeader& slice) {
  pic->first_nal_offset = slice.nal_offset;
  pic->last_nal_offset = slice.nal_offset;
  pic->slice_offsets.clear();
  pic->frame_num = slice.frame_num;
  pic->pps_id = slice.pps_id;
  pic->sps_id = slice.sps_id;
  pic->pic_order_cnt_lsb = slice.pic_order_cnt_lsb;
  pic->idr_pic_id = slice.idr_pic_id;
  pic->nal_ref_idc = slice.nal_ref_idc;
  pic->idr = slice.idr;
  pic->field_pic = slice.field_pic_flag;
  pic->bottom_field = slice.field_pic_flag && slice.bottom_field_flag;
  pic->kind = slice.slice_kind;
  pic->slice_count = 0;
  pic->complete = true;
}

// An IDR picture always begins a new access unit, even when frame_num happens
// to repeat after a wrapped sequence (7.4.1.2.4 / 7.4.2.4).
bool starts_new_picture(const SliceHeader& previous, const SliceHeader& next) {
  if (next.idr != previous.idr) return true;
  return !same_picture_identity(previous, next);
}

}  // namespace

bool same_picture_identity(const SliceHeader& a, const SliceHeader& b) {
  if (a.idr != b.idr) return false;
  if (a.nal_ref_idc != b.nal_ref_idc) return false;
  if (a.pps_id != b.pps_id) return false;
  if (a.frame_num != b.frame_num) return false;
  if (!same_field(a, b)) return false;
  // Separate colour planes are independent coded pictures.
  if (a.colour_plane_id != b.colour_plane_id) return false;
  if (a.idr && a.idr_pic_id != b.idr_pic_id) return false;
  if (a.pic_order_cnt_lsb != b.pic_order_cnt_lsb) return false;
  if (a.delta_pic_order_cnt_bottom != b.delta_pic_order_cnt_bottom) {
    return false;
  }
  if (a.delta_pic_order_cnt0 != b.delta_pic_order_cnt0) return false;
  if (a.delta_pic_order_cnt1 != b.delta_pic_order_cnt1) return false;
  if (a.nal_ref_idc == 0 && b.nal_ref_idc == 0) {
    if (a.redundant_pic_cnt != b.redundant_pic_cnt) return false;
  }
  return true;
}

AuAssembleResult assemble_pictures(const std::vector<SliceHeader>& slices,
                                   const std::vector<bool>& damaged_after,
                                   bool stream_complete) {
  AuAssembleResult result;
  Picture current;
  bool has_current = false;
  bool current_damaged = false;
  SliceHeader previous;

  auto flush = [&](bool closed_normally) {
    if (!has_current) return;
    if (current_damaged) {
      result.incomplete = true;
      result.warnings.push_back(
          "dropped a picture that has a damaged slice");
    } else if (!closed_normally && !stream_complete) {
      result.incomplete = true;
      result.warnings.push_back(
          "dropped an unclosed picture at the end of a damaged stream");
    } else {
      result.pictures.push_back(current);
    }
    has_current = false;
    current_damaged = false;
  };

  for (std::size_t i = 0; i < slices.size(); ++i) {
    const SliceHeader& slice = slices[i];
    const bool damaged =
        i < damaged_after.size() && damaged_after[i];
    if (has_current && !starts_new_picture(previous, slice)) {
      if (slice.slice_kind != current.kind) {
        fail("slices of one picture disagree on slice_type", slice.nal_offset,
             0, true);
      }
      if (slice.pps_id != current.pps_id) {
        fail("slices of one picture disagree on PPS id", slice.nal_offset, 0,
             true);
      }
      current.slice_offsets.push_back(slice.nal_offset);
      current.last_nal_offset = slice.nal_offset;
      ++current.slice_count;
      if (damaged) current_damaged = true;
      previous = slice;
      continue;
    }
    flush(true);
    start_picture(&current, slice);
    current.slice_offsets.push_back(slice.nal_offset);
    ++current.slice_count;
    if (damaged) current_damaged = true;
    has_current = true;
    previous = slice;
  }
  flush(stream_complete || !has_current);
  return result;
}

}  // namespace h264
