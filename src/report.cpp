#include "report.hpp"

#include <cstdio>

namespace tl {

void print_summary_line(const TransitionSummary& summary) {
    // 16 lowercase hex digits for the 64-bit frame hashes and mae with
    // two decimals so the numeric range stays in [0,255].
    std::printf(
        "type=%s frames=%d first_hash=%016llx last_hash=%016llx "
        "mid_changed_pixels=%lld mid_mae=%.2f mismatch_frames=%zu\n",
        transition_type_name(summary.type), summary.frames,
        static_cast<unsigned long long>(summary.first_hash),
        static_cast<unsigned long long>(summary.last_hash),
        summary.mid_changed_pixels, summary.mid_mae,
        summary.mismatch_frames);
}

void print_error(const char* context, const std::string& message) {
    std::fprintf(stderr, "transition_lab: %s: %s\n", context,
                 message.c_str());
}

}  // namespace tl
