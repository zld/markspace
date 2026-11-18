// Coordinate math for SPS dimensions, cropping, and dimension limits.
#pragma once

#include <cstdint>
#include <string>

#include "byte_source.hpp"
#include "sps_types.hpp"

// Kept separate from the syntax parser: this is pure arithmetic over fields
// already decoded, and is where overflow and illegal geometry are caught.
namespace h264inspect {

// Fills sps.coded_width/height, width/height from the parsed macroblock and
// cropping fields. Returns false and fills `diagnostic` when dimensions
// overflow, exceed kMaxDimension, or cropping removes the whole picture.
bool derive_sps_dimensions(Sps& sps, Diagnostic* diagnostic);

}  // namespace h264inspect
