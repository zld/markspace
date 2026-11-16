#ifndef TRANSITION_LAB_REPORT_HPP
#define TRANSITION_LAB_REPORT_HPP

#include <cstdint>
#include <string>

#include "transition.hpp"
#include "verify.hpp"

namespace tl {

struct TransitionSummary {
    TransitionType type = TransitionType::Dissolve;
    int frames = 0;
    std::uint64_t first_hash = 0;
    std::uint64_t last_hash = 0;
    long long mid_changed_pixels = 0;
    double mid_mae = 0.0;
    std::size_t mismatch_frames = 0;
    int first_mismatch = -1;
};

// Print one fixed-field summary line:
// type frames first_hash last_hash mid_changed_pixels mid_mae mismatch_frames
void print_summary_line(const TransitionSummary& summary);

void print_error(const char* context, const std::string& message);

}  // namespace tl

#endif
