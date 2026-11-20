#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

#include "h264/bit_reader.hpp"

namespace h264 {

enum class NalUnitType : int {
  kUnspecified0 = 0,
  kSliceNonIdr = 1,
  kSlicePartitionA = 2,
  kSlicePartitionB = 3,
  kSlicePartitionC = 4,
  kSliceIdr = 5,
  kSei = 6,
  kSps = 7,
  kPps = 8,
  kAccessUnitDelimiter = 9,
  kEndOfSequence = 10,
  kEndOfStream = 11,
  kFiller = 12,
  kSpsExtension = 13,
  kPrefixNal = 14,
  kSubsetSps = 15,
  kDps = 16,
  kSliceAuxiliary = 19,
  kSliceExtension = 20,
  kSliceExtensionDepth = 21,
};

bool is_vcl_type(int type);
bool is_supported_nal_type(int type);
const char* nal_type_name(int type);

struct NalUnit {
  std::size_t offset = 0;
  std::size_t header_size = 0;
  std::size_t payload_offset = 0;
  std::size_t payload_size = 0;
  std::size_t size() const noexcept { return header_size + payload_size; }
  std::size_t next_offset() const noexcept { return offset + size(); }
  int ref_idc = 0;
  int nal_unit_type = 0;

  bool forbidden_zero_bit = false;
};

struct AnnexBScan {
  std::vector<NalUnit> nals;
  std::size_t leading_zero_bytes = 0;
  std::size_t empty_nal_count = 0;
  bool had_truncated_start_code = false;
};

// Scans an Annex-B byte stream. Each NAL's offset is the zero-based file
// offset of its start code (leading_zero_8bits excluded).
AnnexBScan scan_annexb(const std::vector<std::uint8_t>& data,
                       std::size_t max_nal_size);

std::string nal_type_name_string(int type);

}  // namespace h264
