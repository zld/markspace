#ifndef H264_PARAMETER_SETS_HPP
#define H264_PARAMETER_SETS_HPP

#include <cstdint>
#include <map>
#include <vector>

#include "bit_reader.hpp"
#include "diagnostics.hpp"
#include "exp_golomb.hpp"
#include "nal_parser.hpp"

namespace h264 {

// Parsed subset of a sequence parameter set (H.264 7.3.2.1). Only fields
// required for stream structure analysis are retained; VUI and other display
// metadata are skipped by stopping after the frame cropping syntax.
struct SpsInfo {
    unsigned sps_id = 0;
    unsigned profile_idc = 0;
    unsigned constraint_flags = 0;
    unsigned level_idc = 0;
    unsigned chroma_format_idc = 1;       // defaults to 4:2:0
    unsigned bit_depth_luma_minus8 = 0;
    unsigned bit_depth_chroma_minus8 = 0;
    unsigned log2_max_frame_num_minus4 = 0;
    unsigned log2_max_pic_order_cnt_lsb_minus4 = 0;
    unsigned pic_order_cnt_type = 0;
    unsigned max_num_ref_frames = 0;
    bool gaps_in_frame_num_allowed = false;
    unsigned pic_width_in_mbs_minus1 = 0;
    unsigned pic_height_in_map_units_minus1 = 0;
    bool frame_mbs_only_flag = true;
    bool mb_adaptive_frame_field_flag = false;
    bool direct_8x8_inference_flag = false;
    bool frame_cropping_flag = false;
    unsigned crop_left = 0, crop_right = 0, crop_top = 0, crop_bottom = 0;

    // Derived luma sample dimensions after cropping.
    std::uint64_t width = 0;
    std::uint64_t height = 0;
};

// Parsed subset of a picture parameter set (7.3.2.2).
struct PpsInfo {
    unsigned pps_id = 0;
    unsigned sps_id = 0;
    bool entropy_coding_mode_flag = false;
    bool bottom_field_pic_order_in_frame_present = false;
    unsigned num_slice_groups_minus1 = 0;
    unsigned num_ref_idx_l0_default_active_minus1 = 0;
    unsigned num_ref_idx_l1_default_active_minus1 = 0;
    bool weighted_pred_flag = false;
    unsigned weighted_bipred_idc = 0;
    int pic_init_qp_minus26 = 0;
    int pic_init_qs_minus26 = 0;
    int chroma_qp_index_offset = 0;
    bool deblocking_filter_control_present = true;
    bool redundant_pic_cnt_present = false;
};

// Cache of the latest SPS/PPS per id, matching H.264 activation semantics
// for the analyzer's purposes.
class ParameterSetStore {
public:
    void add_sps(const SpsInfo& sps) { sps_[sps.sps_id] = sps; }
    void add_pps(const PpsInfo& pps) { pps_[pps.pps_id] = pps; }

    const SpsInfo* find_sps(unsigned id) const {
        auto it = sps_.find(id);
        return it == sps_.end() ? nullptr : &it->second;
    }
    const PpsInfo* find_pps(unsigned id) const {
        auto it = pps_.find(id);
        return it == pps_.end() ? nullptr : &it->second;
    }

    std::size_t sps_count() const noexcept { return sps_.size(); }
    std::size_t pps_count() const noexcept { return pps_.size(); }

    using SpsMap = std::map<unsigned, SpsInfo>;
    using PpsMap = std::map<unsigned, PpsInfo>;
    const SpsMap& all_sps() const noexcept { return sps_; }
    const PpsMap& all_pps() const noexcept { return pps_; }

private:
    SpsMap sps_;
    PpsMap pps_;
};

// Returns true and stores the result on success; on malformed syntax records
// a diagnostic carrying the NAL file offset and returns false.
bool parse_sps(const NalUnit& nal, SpsInfo& out, Diagnostics& diag);
bool parse_pps(const NalUnit& nal, PpsInfo& out, Diagnostics& diag);

}  // namespace h264

#endif
