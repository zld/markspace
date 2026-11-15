#ifndef H264_EXP_GOLOMB_HPP
#define H264_EXP_GOLOMB_HPP

#include "bit_reader.hpp"

#include <cstdint>

namespace h264 {

// Unsigned Exp-Golomb (ue(v)). Leading-zero runs longer than
// maxLeadingZeros or reads past the buffer end set the reader error flag and
// return 0.
std::uint32_t decodeUe(BitReader& reader, int maxLeadingZeros = 31);

// Signed Exp-Golomb (se(v)); codeNum k maps to (k+1)/2 for odd k and
// -(k/2) for even k. Shares the leading-zero limit of the unsigned decoder.
std::int32_t decodeSe(BitReader& reader, int maxLeadingZeros = 31);

} // namespace h264

#endif
