// SPS syntax parsing.
#pragma once

#include <cstdint>

#include "exp_golomb.hpp"
#include "sps_types.hpp"

namespace h264inspect {

// Parses one SPS NAL payload. `payload` points at the first byte after the
// NAL header. On failure `out` holds all fields decoded before the error and
// `diagnostic` describes the failure at its absolute file offset.
bool parse_sps(const std::uint8_t* payload, std::size_t payload_size,
               std::uint64_t payload_offset, std::uint64_t nal_offset,
               ExpGolombStats& stats, Sps& out, Diagnostic* diagnostic);

}  // namespace h264inspect
