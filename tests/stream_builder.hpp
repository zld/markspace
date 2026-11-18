// Complete Annex B stream synthesis for analyzer tests and --generate-sample.
#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "annexb_fixture.hpp"
#include "pps_slice_fixture.hpp"
#include "sps_fixture.hpp"

namespace selftest {

// One VCL access unit made of one or more slices.
struct SliceSpec {
    std::uint32_t first_mb = 0;
    std::uint32_t slice_type = 2;  // I
    std::uint32_t frame_num = 0;
    std::uint32_t pic_order_cnt_lsb = 0;
    std::uint32_t idr_pic_id = 0;
    bool is_idr = true;
    int nal_ref_idc = 3;
    bool field_pic = false;
    bool bottom_field = false;
    std::uint32_t pps_id = 0;
};

// Concatenates SPS, PPS and access units into an Annex B byte stream.
inline std::vector<std::uint8_t> build_stream(
    const SpsFixture& sps, const PpsFixture& pps,
    const std::vector<std::vector<SliceSpec>>& access_units,
    bool four_byte = true) {
    std::vector<std::uint8_t> bytes;
    append(bytes, build_sps_nal(sps, four_byte));
    append(bytes, build_pps_nal(pps, four_byte));
    for (const auto& au : access_units) {
        for (const SliceSpec& s : au) {
            SliceFixture slice;
            slice.first_mb_in_slice = s.first_mb;
            slice.slice_type = s.slice_type;
            slice.pps_id = s.pps_id;
            slice.frame_num = s.frame_num;
            slice.pic_order_cnt_lsb = s.pic_order_cnt_lsb;
            slice.idr_pic_id = s.idr_pic_id;
            slice.is_idr = s.is_idr;
            slice.nal_ref_idc = s.nal_ref_idc;
            slice.field_pic_flag = s.field_pic;
            slice.bottom_field_flag = s.bottom_field;
            // IDR headers carry nal_ref_idc = 3 and non-zero idr_pic_id when set.
            const int header = (s.nal_ref_idc << 5) | (s.is_idr ? 5 : 1);
            const std::vector<std::uint8_t> rbsp = escape_rbsp(
                build_slice_header_rbsp(slice, sps, pps));
            append(bytes, annexb_nal(header, rbsp, four_byte));
        }
    }
    return bytes;
}

// Deterministic reference-sample stream used by the CLI demo and the tests:
// one IDR I frame with three slices, then one P frame and one B frame.
inline std::vector<std::uint8_t> build_reference_sample() {
    SpsFixture sps;
    sps.profile_idc = 100;
    sps.level_idc = 31;
    sps.pic_width_in_mbs_minus1 = 19;         // 320 px
    sps.pic_height_in_map_units_minus1 = 11;  // 192 px
    sps.log2_max_pic_order_cnt_lsb_minus4 = 2;

    PpsFixture pps;
    pps.deblocking_filter_control_present = true;

    std::vector<std::vector<SliceSpec>> access_units;
    // One IDR picture carrying a single slice, then a P frame and a B frame.
    access_units.push_back({{0, 2, 0, 0, 1, true, 3, false, false, 0}});
    access_units.push_back({{0, 0, 1, 4, 0, false, 2, false, false, 0}});  // P
    access_units.push_back({{0, 1, 2, 8, 0, false, 0, false, false, 0}});  // B
    return build_stream(sps, pps, access_units);
}

}  // namespace selftest
