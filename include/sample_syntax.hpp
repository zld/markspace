// Shared syntax builders for generated H.264 sample streams.
#pragma once

#include <cstdint>
#include <vector>

namespace h264inspect {
namespace samplegen {

constexpr std::uint32_t kProfileIdc = 100;
constexpr std::uint32_t kLevelIdc = 31;
constexpr std::uint32_t kPicWidthInMbsMinus1 = 19;         // 320 px
constexpr std::uint32_t kPicHeightInMapUnitsMinus1 = 11;   // 192 px frame map
constexpr std::uint32_t kLog2MaxFrameNumMinus4 = 0;
constexpr std::uint32_t kLog2MaxPocLsbMinus4 = 2;

struct SliceSpec {
    std::uint32_t first_mb;
    std::uint32_t slice_type;
    std::uint32_t frame_num;
    std::uint32_t poc_lsb;
    bool idr;
    int nal_ref_idc;
    bool field_pic;
    bool bottom_field;
};

// Appends an Annex B NAL with emulation-prevention bytes applied.
void append_nal(std::vector<std::uint8_t>& out, std::uint8_t header,
                const std::vector<std::uint8_t>& rbsp);

std::vector<std::uint8_t> build_sps(bool interlaced);
std::vector<std::uint8_t> build_pps();
std::vector<std::uint8_t> build_slice(const SliceSpec& spec);
std::uint8_t slice_header_byte(int nal_ref_idc, bool idr);

}  // namespace samplegen
}  // namespace h264inspect
