// Slice types shared by slice parsing and reporting.
#pragma once

#include <cstdint>

namespace h264inspect {

enum class SliceType { P = 0, B = 1, I = 2, Sp = 3, Si = 4, Unknown = 5 };

const char* slice_type_name(SliceType type);

// s->0 P, s->1 B, s->2 I, s->3 SP, s->4 SI.
SliceType normalize_slice_type(std::uint32_t raw);

}  // namespace h264inspect
