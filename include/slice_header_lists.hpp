// Optional slice-header lists that the analyzer must skip to stay in sync.
#pragma once

#include <cstdint>

#include "bit_reader.hpp"
#include "exp_golomb.hpp"
#include "byte_source.hpp"
#include "pps_parser.hpp"
#include "slice_types.hpp"
#include "sps_types.hpp"

namespace h264inspect {

// Skips ref_pic_list_modification() for one list.
bool skip_ref_pic_list_modification(BitReader& reader, ExpGolombDecoder& decoder,
                                    Diagnostic* diagnostic);

// Skips pred_weight_table() for the slice type using the PPS defaults.
bool skip_pred_weight_table(BitReader& reader, ExpGolombDecoder& decoder,
                            const Sps& sps, const Pps& pps,
                            std::uint32_t slice_type, Diagnostic* diagnostic);

// Skips dec_ref_pic_marking() for IDR and non-IDR reference slices.
bool skip_dec_ref_pic_marking(BitReader& reader, ExpGolombDecoder& decoder,
                              bool is_idr, Diagnostic* diagnostic);

}  // namespace h264inspect
