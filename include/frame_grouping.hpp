// Folding coded pictures into frames and frames into image groups.
#pragma once

#include <vector>

#include "analysis_result.hpp"

namespace h264inspect {

// Pairs complementary top/bottom fields of the same frame where possible and
// leaves unmatched fields marked as isolated.
std::vector<FrameInfo> fold_frames(const std::vector<PictureInfo>& pictures);

// Splits frames into image groups at first picture, IDR, and activated
// parameter-set changes, then recomputes completeness.
std::vector<GopGroup> group_frames(const std::vector<FrameInfo>& frames);

}  // namespace h264inspect
