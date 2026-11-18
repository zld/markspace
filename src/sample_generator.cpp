#include "sample_generator.hpp"

#include <vector>

#include "sample_syntax.hpp"

namespace h264inspect {

namespace samplegen {
namespace {

std::vector<std::uint8_t> build_sample(const std::vector<SliceSpec>& slices,
                                       bool interlaced) {
    std::vector<std::uint8_t> out;
    append_nal(out, 0x67, build_sps(interlaced));
    append_nal(out, 0x68, build_pps());
    for (const SliceSpec& spec : slices) {
        append_nal(out, slice_header_byte(spec.nal_ref_idc, spec.idr),
                   build_slice(spec));
    }
    return out;
}

}  // namespace
}  // namespace samplegen

std::vector<std::uint8_t> generate_reference_sample() {
    // One IDR I picture with a single slice, then one P frame and one B frame.
    const std::vector<samplegen::SliceSpec> slices = {
        {0, 2, 0, 0, true, 3, false, false},
        {0, 0, 1, 4, false, 2, false, false},
        {0, 1, 2, 8, false, 0, false, false},
    };
    return samplegen::build_sample(slices, false);
}

std::vector<std::uint8_t> generate_interlaced_sample() {
    const std::vector<samplegen::SliceSpec> slices = {
        {0, 2, 0, 0, true, 3, true, false},   // top field
        {0, 2, 0, 0, true, 3, true, true},    // bottom field
        {0, 0, 1, 4, false, 2, true, false},  // top field of P frame
        {0, 0, 1, 4, false, 2, true, true},   // bottom field of P frame
    };
    return samplegen::build_sample(slices, true);
}

}  // namespace h264inspect
