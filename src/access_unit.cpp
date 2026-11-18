#include "access_unit.hpp"

#include <string>
#include <utility>
#include <vector>

#include "bit_reader.hpp"
#include "boundary_rules.hpp"
#include "diagnostics.hpp"
#include "frame_grouping.hpp"
#include "param_set_store.hpp"

namespace h264inspect {

AnalysisResult analyze_stream(const ByteSource& source) {
    AnalysisResult result;
    const NalScanResult scan = scan_nal_units(source);
    result.nal_summary = scan.summary;
    result.nal_count = static_cast<std::uint32_t>(scan.units.size());
    for (const Diagnostic& d : scan.diagnostics) result.errors.push_back(d);
    if (scan.units.empty()) return result;

    ParamSetStore store;
    std::vector<PictureInfo> pictures;
    PictureInfo current;
    // Offset of the first NAL after the last VCL of the previous picture; this
    // is where the next access unit starts, covering SPS/PPS/SEI/AUD prefixes.
    std::uint64_t pending_au_start = scan.units.front().start_code_offset;
    bool pending_au_start_set = true;
    bool previous_slice_broken = false;
    std::string pending_uncertainty;

    auto flush_picture = [&]() {
        if (current.slice_count == 0) {
            current = PictureInfo();
            return;
        }
        if (!current.reference_consistent) {
            add_diagnostic(result.errors, ErrorKind::IllegalValue, current.first_nal_offset,
                      "nal_ref_idc varies between slices of one picture");
            current.uncertain = true;
            if (current.uncertainty.empty()) {
                current.uncertainty =
                    "slices of this picture disagree on reference status";
            }
        }
        pictures.push_back(current);
        current = PictureInfo();
    };

    for (const NalUnit& unit : scan.units) {
        const std::uint64_t payload_offset = unit.payload_offset + 1;
        const std::uint8_t* payload =
            source.data() + static_cast<std::size_t>(payload_offset);
        if (unit.payload_length > kMaxNalSize) continue;
        const int type = unit.unit_type();

        if (type == 7) {
            Sps sps;
            Diagnostic diag;
            const bool ok = parse_sps(payload, unit.payload_length, payload_offset,
                                      unit.start_code_offset, result.exp_golomb,
                                      sps, &diag);
            if (!ok) {
                result.errors.push_back(diag);
                continue;
            }
            const ParamSetImage image =
                make_param_set_image(payload, unit.payload_length, payload_offset);
            store.store_sps(sps, image);
            result.sps_list.push_back(sps);
            if (!pending_au_start_set) {
                pending_au_start = unit.start_code_offset;
                pending_au_start_set = true;
            }
            continue;
        }
        if (type == 8) {
            Pps pps;
            Diagnostic diag;
            const bool ok = parse_pps(payload, unit.payload_length, payload_offset,
                                      unit.start_code_offset, result.exp_golomb,
                                      pps, &diag);
            if (!ok) {
                result.errors.push_back(diag);
                continue;
            }
            const ParamSetImage image =
                make_param_set_image(payload, unit.payload_length, payload_offset);
            store.store_pps(pps, image);
            result.pps_list.push_back(pps);
            if (!pending_au_start_set) {
                pending_au_start = unit.start_code_offset;
                pending_au_start_set = true;
            }
            continue;
        }
        if (!unit.is_vcl()) {
            // SEI, AUD, filler and other non-VCL units belong to the AU that
            // follows them, so they only seed its start offset.
            if (!pending_au_start_set) {
                pending_au_start = unit.start_code_offset;
                pending_au_start_set = true;
            }
            if (type == 13 || type == 15) {
                add_diagnostic(result.errors, ErrorKind::Unsupported, unit.start_code_offset,
                          std::string("SPS extension NAL (type ") +
                              std::to_string(type) + ") is not analyzed");
            }
            continue;
        }
        if (unit.is_unsupported_vcl()) {
            add_diagnostic(result.errors, ErrorKind::Unsupported, unit.start_code_offset,
                      std::string("VCL NAL type ") + std::to_string(type) + " (" +
                          nal_unit_type_name(type) +
                          ") uses a slice header layout this analyzer does not model");
            if (!pending_au_start_set) {
                pending_au_start = unit.start_code_offset;
                pending_au_start_set = true;
            }
            previous_slice_broken = true;
            continue;
        }
        if (unit.payload_length == 0) {
            // Reported by the NAL scanner; it carries no usable header.
            previous_slice_broken = true;
            continue;
        }

        SlicePrefix prefix;
        ExpGolombStats scratch_stats;
        const bool have_prefix = read_slice_prefix(
            payload, unit.payload_length, payload_offset, scratch_stats, prefix);
        if (!have_prefix) {
            add_diagnostic(result.errors, ErrorKind::Truncated, payload_offset,
                      "slice header too short to read its leading fields");
            previous_slice_broken = true;
            if (current.slice_count > 0) {
                current.truncated = true;
                current.uncertain = true;
                current.uncertainty =
                    "slice at offset " + std::to_string(unit.start_code_offset) +
                    " has an unreadable header";
            } else {
                pending_uncertainty =
                    "picture starting near offset " +
                    std::to_string(unit.start_code_offset) +
                    " contains an unreadable slice header";
            }
            continue;
        }

        const Pps* pps = store.find_pps(prefix.pps_id);
        const Sps* sps = pps == nullptr ? nullptr : store.find_sps(pps->sps_id);
        if (pps == nullptr || sps == nullptr) {
            if (pps == nullptr) {
                add_diagnostic(result.errors, ErrorKind::UnknownPps, unit.start_code_offset,
                          "slice references undefined PPS " +
                              std::to_string(prefix.pps_id));
            } else {
                add_diagnostic(result.errors, ErrorKind::UnknownSps, unit.start_code_offset,
                          "PPS " + std::to_string(pps->id) + " references undefined SPS " +
                              std::to_string(pps->sps_id));
            }
            previous_slice_broken = true;
            const std::string note =
                "slice at offset " + std::to_string(unit.start_code_offset) +
                " uses an unknown parameter set";
            if (current.slice_count > 0) {
                ++current.slice_count;
                current.truncated = true;
                current.uncertain = true;
                current.uncertainty = note;
            } else {
                // Without a parameter set the field layout is unknown, so only
                // the peeked prefix and NAL header can be reported.
                PictureInfo degraded = degraded_picture(
                    prefix.slice_type_raw, type == 5, unit.nal_ref_idc(),
                    prefix.pps_id, 0, 0, 0, prefix.first_mb,
                    unit.start_code_offset,
                    pending_au_start_set ? pending_au_start
                                         : unit.start_code_offset,
                    pending_uncertainty.empty()
                        ? note
                        : pending_uncertainty + "; " + note);
                pending_au_start_set = false;
                pending_uncertainty.clear();
                pictures.push_back(std::move(degraded));
            }
            continue;
        }

        const std::uint32_t sps_revision = store.sps_revision(sps->id);
        const std::uint32_t pps_revision = store.pps_revision(pps->id);
        SliceContext context;
        context.sps = sps;
        context.pps = pps;

        SliceHeader slice;
        Diagnostic diag;
        const bool ok = parse_slice_header(
            payload, unit.payload_length, payload_offset, unit.nal_ref_idc(), type,
            context, result.exp_golomb, slice, &diag);
        if (!ok) {
            result.errors.push_back(diag);
            previous_slice_broken = true;
            const std::string note =
                "slice at offset " + std::to_string(unit.start_code_offset) +
                " could not be parsed: " + diag.message;
            if (current.slice_count > 0) {
                // The slice belongs to the picture already open; count it so
                // the picture is not under-reported, and flag the ambiguity.
                ++current.slice_count;
                current.truncated = true;
                current.uncertain = true;
                current.uncertainty = note;
            } else {
                // Preserve what the peeked prefix established so the picture is
                // still reported, clearly marked as partially analyzed.
                PictureInfo degraded = degraded_picture(
                    prefix.slice_type_raw, type == 5, unit.nal_ref_idc(),
                    prefix.pps_id, sps->id, sps_revision, pps_revision,
                    prefix.first_mb, unit.start_code_offset,
                    pending_au_start_set ? pending_au_start
                                         : unit.start_code_offset,
                    pending_uncertainty.empty()
                        ? note
                        : pending_uncertainty + "; " + note);
                pending_au_start_set = false;
                pending_uncertainty.clear();
                pictures.push_back(std::move(degraded));
            }
            continue;
        }

        const BoundaryDecision decision = decide_boundary(
            current, slice, unit.nal_ref_idc(), sps_revision, pps_revision,
            previous_slice_broken);
        if (decision.new_picture) {
            flush_picture();
            previous_slice_broken = false;
            current.au_start_offset =
                pending_au_start_set ? pending_au_start : unit.start_code_offset;
            pending_au_start_set = false;
            if (!pending_uncertainty.empty()) {
                current.uncertain = true;
                current.uncertainty = pending_uncertainty;
                pending_uncertainty.clear();
            }
        }
        if (!slice.header_complete) {
            // Identity fields were readable; only deeper header syntax is
            // missing, which usually means the slice was cut short.
            add_diagnostic(result.errors, ErrorKind::Truncated, unit.start_code_offset,
                      "slice header ends before slice data begins");
            current.truncated = true;
        }
        if (decision.uncertain) {
            // The slice cannot be cleanly separated from the current picture,
            // so it stays put and the ambiguity is recorded rather than guessed.
            current.uncertain = true;
            if (current.uncertainty.empty()) current.uncertainty = decision.reason;
        }
        apply_slice(current, slice, unit.nal_ref_idc(), sps->id, sps_revision,
                    pps_revision, unit.start_code_offset);
    }
    flush_picture();

    result.frames = fold_frames(pictures);
    result.groups = group_frames(result.frames);

    result.pictures = std::move(pictures);
    return result;
}

}  // namespace h264inspect
