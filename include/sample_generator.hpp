// Deterministic Annex B sample generation for the CLI demo and tests.
#pragma once

#include <cstdint>
#include <vector>

namespace h264inspect {

// Builds the reference sample used by the acceptance demo: one IDR picture of
// type I, followed by one P frame and one B frame, three slices total, at
// 320x192 with 4:2:0 8-bit high-profile SPS syntax.
std::vector<std::uint8_t> generate_reference_sample();

// Builds the same stream but marks the SPS as interlaced, so the demo can
// exercise field/frame reporting (top+bottom field pair, one picture each).
std::vector<std::uint8_t> generate_interlaced_sample();

}  // namespace h264inspect
