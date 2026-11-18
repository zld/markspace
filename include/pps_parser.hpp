// Picture parameter set parsing.
#pragma once

#include <cstdint>
#include <string>

#include "bit_reader.hpp"
#include "exp_golomb.hpp"

namespace h264inspect {

struct Pps {
    std::uint32_t id = 0;
    std::uint32_t sps_id = 0;
    bool entropy_coding_mode = false;  // 0 = CAVLC, 1 = CABAC
    bool bottom_field_pic_order_in_frame_present = false;
    std::uint32_t num_slice_groups_minus1 = 0;
    std::uint32_t num_ref_idx_l0_default_active = 1;
    std::uint32_t num_ref_idx_l1_default_active = 1;
    bool weighted_pred = false;
    std::uint32_t weighted_bipred_idc = 0;
    std::int64_t pic_init_qp_minus26 = 0;
    std::int64_t pic_init_qs_minus26 = 0;
    std::int64_t chroma_qp_index_offset = 0;
    bool deblocking_filter_control_present = false;
    bool constrained_intra_pred = false;
    bool redundant_pic_cnt_present = false;

    std::uint64_t nal_offset = 0;
    std::uint64_t pps_byte_offset = 0;
};

bool parse_pps(const std::uint8_t* payload, std::size_t payload_size,
               std::uint64_t payload_offset, std::uint64_t nal_offset,
               ExpGolombStats& stats, Pps& out, Diagnostic* diagnostic);

}  // namespace h264inspect
