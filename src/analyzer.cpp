#include "h264/analyzer.hpp"

#include <sstream>

namespace h264 {

namespace {

void record_error(const Diagnostic& diag, const AnalysisOptions& options,
                  AnalysisReport* report) {
  report->errors.push_back(diag);
  std::ostringstream oss;
  oss << "NAL offset " << diag.nal_offset << ": " << diag.message;
  report->warnings.push_back(oss.str());
  if (options.mode == AnalyzeMode::kStrict) throw ParseError(diag);
}

}  // namespace

AnalysisReport analyze_stream(const std::vector<std::uint8_t>& data,
                              const AnalysisOptions& options) {
  AnalysisReport report;
  report.scan = scan_annexb(data, options.max_nal_size);
  report.leading_zero_bytes = report.scan.leading_zero_bytes;
  report.empty_nal_count = report.scan.empty_nal_count;
  report.had_truncated_start_code = report.scan.had_truncated_start_code;
  if (report.scan.nals.empty()) {
    // Nothing was analyzable: an empty file, leading zeros only, or a stream
    // with no complete Annex-B start code.
    report.incomplete = true;
    report.warnings.push_back("no Annex-B NAL unit found in the input");
    report.ok = false;
    return report;
  }

  std::vector<SliceHeader> slices;
  std::vector<bool> damaged_picture;
  bool unassigned_vcl_damage = false;
  bool has_vcl = false;

  for (const NalUnit& nal : report.scan.nals) {
    const int type = nal.nal_unit_type;
    try {
      if (nal.forbidden_zero_bit) {
        fail("forbidden_zero_bit is set", nal.offset, 0, true);
      }
      if (!is_supported_nal_type(type)) {
        if (type == 2 || type == 3 || type == 4) {
          fail("data partitioning is not supported (NAL type " +
                   std::to_string(type) + ")",
               nal.offset, 0, true);
        }
        if (type == 13 || type == 14 || type == 15 || type == 16 ||
            (type >= 19 && type <= 21)) {
          fail("unsupported extension syntax (NAL type " +
                   std::to_string(type) + ")",
               nal.offset, 0, true);
        }
        if (type == 0) {
          ++report.unknown_nal_type_count;
          continue;
        }
        fail("reserved NAL unit type " + std::to_string(type), nal.offset, 0,
             true);
      }
      if (type == 5 && nal.ref_idc == 0) {
        fail("IDR slice with nal_ref_idc == 0", nal.offset, 0, true);
      }
      if (type == 7 && nal.ref_idc == 0) {
        fail("SPS with nal_ref_idc == 0", nal.offset, 0, true);
      }
      const std::uint8_t* payload = data.data() + nal.payload_offset;
      const std::size_t size = nal.payload_size;
      if (type == 7) {
        const Sps sps = parse_sps(payload, size, nal.offset);
        if (report.sets.sps.count(sps.sps_id) != 0) {
          report.warnings.push_back("SPS id " + std::to_string(sps.sps_id) +
                                    " updated");
        }
        report.sets.sps[sps.sps_id] = sps;
      } else if (type == 8) {
        const Pps pps = parse_pps(payload, size, nal.offset, report.sets);
        if (report.sets.pps.count(pps.pps_id) != 0) {
          report.warnings.push_back("PPS id " + std::to_string(pps.pps_id) +
                                    " updated");
        }
        report.sets.pps[pps.pps_id] = pps;
      } else if (is_vcl_type(type)) {
        const SliceHeader slice = parse_slice_header(
            payload, size, type, nal.ref_idc, nal.offset, report.sets);
        if (unassigned_vcl_damage && !damaged_picture.empty()) {
          damaged_picture.back() = true;
          unassigned_vcl_damage = false;
        }
        slices.push_back(slice);
        damaged_picture.push_back(false);
        has_vcl = true;
      }
    } catch (const ParseError& error) {
      if (is_vcl_type(type)) {
        // A slice we cannot parse may belong to the picture that just began.
        unassigned_vcl_damage = true;
      }
      record_error(error.diagnostic(), options, &report);
    }
  }
  if (unassigned_vcl_damage && !damaged_picture.empty()) {
    damaged_picture.back() = true;
  }
  if (!has_vcl) {
    report.incomplete = true;
    report.warnings.push_back("no VCL NAL unit (slice) found in the input");
  }

  const bool stream_complete = !report.scan.had_truncated_start_code;
  const AuAssembleResult assembled =
      assemble_pictures(slices, damaged_picture, stream_complete);
  report.pictures = assembled.pictures;
  // Any recovered error, missing VCL or truncated/scanned tail means part of
  // the stream was not analyzed, so the report is incomplete even if every
  // remaining picture assembled cleanly.
  report.incomplete = assembled.incomplete ||
                      report.scan.had_truncated_start_code ||
                      !report.errors.empty() || !has_vcl;
  report.warnings.insert(report.warnings.end(), assembled.warnings.begin(),
                         assembled.warnings.end());

  report.gops = build_gop_index(report.pictures);
  report.stats = compute_stats(data, report.scan.nals, report.sets,
                               report.pictures, report.gops,
                               report.incomplete);
  report.ok = report.errors.empty() && !report.incomplete;
  return report;
}

}  // namespace h264
