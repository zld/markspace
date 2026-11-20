#include "h264/nal.hpp"

#include <cstdio>

namespace h264 {

bool is_vcl_type(int type) { return type >= 1 && type <= 5; }

bool is_supported_nal_type(int type) {
  switch (type) {
    case 1:   // non-IDR slice
    case 5:   // IDR slice
    case 6:   // SEI
    case 7:   // SPS
    case 8:   // PPS
    case 9:   // access unit delimiter
    case 10:  // end of sequence
    case 11:  // end of stream
    case 12:  // filler
      return true;
    case 2:
    case 3:
    case 4:   // data partitioning is not supported
    case 13:
    case 14:
    case 15:
    case 16:
    case 19:
    case 20:
    case 21:  // extension / auxiliary syntax is not supported
      return false;
    default:
      return false;
  }
}

const char* nal_type_name(int type) {
  switch (type) {
    case 1: return "slice_non_idr";
    case 2: return "slice_partition_a";
    case 3: return "slice_partition_b";
    case 4: return "slice_partition_c";
    case 5: return "slice_idr";
    case 6: return "sei";
    case 7: return "sps";
    case 8: return "pps";
    case 9: return "access_unit_delimiter";
    case 10: return "end_of_sequence";
    case 11: return "end_of_stream";
    case 12: return "filler";
    case 13: return "sps_extension";
    case 14: return "prefix_nal";
    case 15: return "subset_sps";
    case 16: return "dps";
    case 19: return "slice_auxiliary";
    case 20: return "slice_extension";
    case 21: return "slice_extension_depth";
    default: return "reserved_or_unknown";
  }
}

std::string nal_type_name_string(int type) { return nal_type_name(type); }

namespace {

// Locates the next 00 00 01 at or after `from`.
//
// A zero byte directly before the prefix is folded into a four-byte start
// code (zero_byte / leading_zero_8bits) when allowed. Folding is suppressed
// for the first prefix so its absolute file offset stays exact (the extra
// zeros there are leading_zero_8bits), and it is suppressed when the prefix
// starts exactly at `from`, because that byte may be the header of an empty
// NAL.
bool find_prefix(const std::vector<std::uint8_t>& data, std::size_t from,
                 bool fold_zero_byte, std::size_t* code_pos,
                 std::size_t* code_len) {
  for (std::size_t i = from; i + 2 < data.size(); ++i) {
    if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) {
      if (fold_zero_byte && i > from && data[i - 1] == 0) {
        *code_pos = i - 1;
        *code_len = 4;
      } else {
        *code_pos = i;
        *code_len = 3;
      }
      return true;
    }
  }
  return false;
}

}  // namespace

AnnexBScan scan_annexb(const std::vector<std::uint8_t>& data,
                       std::size_t max_nal_size) {
  AnnexBScan scan;
  std::size_t code_pos = 0, code_len = 0;
  if (!find_prefix(data, 0, true, &code_pos, &code_len)) {
    // No complete start code. Trailing 00 / 00 00 / 00 00 01 is truncated.
    std::size_t i = data.size();
    while (i > 0 && data[i - 1] == 0) --i;
    scan.had_truncated_start_code =
        (data.size() - i) >= 2 && data.size() > 0;
    if (data.size() >= 3 && data[data.size() - 3] == 0 &&
        data[data.size() - 2] == 0 && data[data.size() - 1] == 1) {
      scan.had_truncated_start_code = true;
    }
    return scan;
  }

  // Anything before the first start code is leading_zero_8bits. With a
  // four-byte prefix the header starts after all four bytes, so the reported
  // offset is the exact absolute position of the code.
  scan.leading_zero_bytes = code_pos;

  while (true) {
    const std::size_t header_offset = code_pos + code_len;
    if (header_offset >= data.size()) {
      // A start code at end of file: the prefix itself is complete, but the
      // NAL unit it introduces is missing.
      scan.had_truncated_start_code = true;
      break;
    }

    std::size_t next_pos = 0, next_len = 0;
    const bool has_next =
        find_prefix(data, header_offset, true, &next_pos, &next_len);

    // A start code found immediately after the header means the NAL is empty.
    // Its start code is still recorded so every offset stays addressable.
    if (has_next && next_pos == header_offset) {
      ++scan.empty_nal_count;
      NalUnit empty;
      empty.offset = code_pos;
      empty.header_size = 1;
      empty.payload_offset = header_offset + 1;
      empty.payload_size = 0;
      empty.nal_unit_type = data[header_offset] & 0x1F;
      empty.ref_idc = (data[header_offset] >> 5) & 0x03;
      scan.nals.push_back(empty);
      code_pos = next_pos;
      code_len = next_len;
      continue;
    }
    std::size_t payload_end = has_next ? next_pos : data.size();

    // Trailing zero bytes before the next start code are trailing_zero_8bits,
    // not part of the NAL payload.
    while (payload_end > header_offset + 1 && data[payload_end - 1] == 0) {
      --payload_end;
    }

    NalUnit nal;
    nal.offset = code_pos;
    nal.header_size = 1;
    nal.payload_offset = header_offset + 1;
    nal.payload_size =
        payload_end > nal.payload_offset ? payload_end - nal.payload_offset : 0;
    const std::uint8_t header = data[header_offset];
    nal.forbidden_zero_bit = (header & 0x80u) != 0;
    nal.ref_idc = (header >> 5) & 0x03;
    nal.nal_unit_type = header & 0x1F;

    if (nal.payload_size == 0) ++scan.empty_nal_count;
    if (nal.size() > max_nal_size) {
      fail("NAL unit exceeds the 64 MiB size limit", nal.offset, 0, true);
    }
    scan.nals.push_back(nal);

    if (!has_next) break;
    code_pos = next_pos;
    code_len = next_len;
  }
  return scan;
}

}  // namespace h264
