#include "access_unit.hpp"

#include <algorithm>
#include <unordered_map>

namespace h264 {
namespace {

struct PpsBinding {
    std::uint32_t sps_id = 0;
    bool present = false;
};

// Frame-coded pictures are identified by frame_num; field-coded pictures by
// frame_num plus the parity of their field.  PPS id participates as well so
// streams that reuse frame_num across independent parameter sets stay apart.
struct PictureKey {
    std::uint32_t pps_id;
    std::uint32_t frame_num;
    bool field_pic;
    bool bottom_field;
    bool idr;

    bool operator==(const PictureKey& other) const {
        return pps_id == other.pps_id && frame_num == other.frame_num &&
               field_pic == other.field_pic &&
               bottom_field == other.bottom_field && idr == other.idr;
    }
};

struct KeyHash {
    std::size_t operator()(const PictureKey& k) const noexcept {
        std::uint64_t h = k.frame_num;
        h = h * 1000003u ^ k.pps_id;
        h = h * 1000003u ^ static_cast<std::uint32_t>(k.field_pic);
        h = h * 1000003u ^ static_cast<std::uint32_t>(k.bottom_field);
        h = h * 1000003u ^ static_cast<std::uint32_t>(k.idr);
        return static_cast<std::size_t>(h);
    }
};

PictureKey key_of(const SliceInfo& s) {
    return {s.pic_parameter_set_id, s.frame_num, s.field_pic_flag,
            s.bottom_field_flag, s.idr};
}

void count_image(const ImageRecord& image, PictureGroup& group) {
    ++group.image_count;
    if (image.picture_type == PictureType::kI) ++group.i_count;
    else if (image.picture_type == PictureType::kP) ++group.p_count;
    else if (image.picture_type == PictureType::kB) ++group.b_count;
}

}  // namespace

StreamSummary analyze_stream(const std::uint8_t* data, std::size_t size) {
    StreamSummary summary;
    ScanResult scan = scan_annex_b(data, size);
    summary.diagnostics = std::move(scan.diagnostics);

    std::unordered_map<std::uint32_t, SpsInfo> sps_sets;
    std::unordered_map<std::uint32_t, PpsBinding> pps_sets;
    const SpsInfo* active_sps = nullptr;

    // Maps an in-progress picture to its index in summary.images so that
    // follow-on slices of the same picture are merged instead of counted as
    // new images.
    std::unordered_map<PictureKey, std::size_t, KeyHash> open_pictures;

    for (std::size_t i = 0; i < scan.nal_units.size(); ++i) {
        const NalUnit& nal = scan.nal_units[i];

        if (nal.empty) {
            continue;  // already diagnosed by the scanner
        }
        if (nal.forbidden_zero_bit) {
            summary.diagnostics.error(
                nal.header_offset, "forbidden_zero_bit",
                "NAL header forbidden_zero_bit is set at offset " +
                    std::to_string(nal.header_offset));
            continue;
        }

        switch (nal.unit_type) {
            case kNalSps: {
                SpsInfo parsed = parse_sps(nal, data, summary.diagnostics);
                sps_sets[parsed.seq_parameter_set_id] = parsed;
                if (parsed.valid) {
                    summary.sps = parsed;
                    active_sps = &summary.sps;
                }
                break;
            }
            case kNalPps: {
                PpsInfo parsed = parse_pps(nal, data, summary.diagnostics);
                if (parsed.present) {
                    pps_sets[parsed.pic_parameter_set_id] =
                        PpsBinding{parsed.seq_parameter_set_id, true};
                }
                break;
            }
            case kNalAccessUnitDelimiter:
                // An AUD closes every in-progress picture identity so slices
                // after it can never be merged into a picture before it.
                open_pictures.clear();
                break;
            case kNalNonIdrSlice:
            case kNalIdrSlice: {
                const PpsBinding* binding = nullptr;
                SliceInfo slice = parse_slice_header(
                    nal, i, data, active_sps, summary.diagnostics);
                auto bound = pps_sets.find(slice.pic_parameter_set_id);
                if (bound != pps_sets.end()) binding = &bound->second;
                if (!slice.valid) break;
                if (binding == nullptr) {
                    summary.diagnostics.error(
                        nal.start_offset, "missing_parameter_set",
                        "slice references unknown PPS id " +
                            std::to_string(slice.pic_parameter_set_id));
                } else {
                    auto sps_it = sps_sets.find(binding->sps_id);
                    if (sps_it != sps_sets.end() && sps_it->second.valid) {
                        active_sps = &sps_it->second;
                    }
                }

                PictureKey key = key_of(slice);
                auto found = open_pictures.find(key);
                if (found != open_pictures.end()) {
                    ImageRecord& image = summary.images[found->second];
                    image.slice_nal_offsets.push_back(nal.start_offset);
                    if (slice.picture_type == PictureType::kUnknown) {
                        ++image.unknown_slice_count;
                        ++summary.unknown_slice_count;
                    } else if (image.picture_type != PictureType::kUnknown &&
                               image.picture_type != slice.picture_type) {
                        summary.diagnostics.error(
                            nal.start_offset, "slice_type_conflict",
                            "later slice of one picture reports a different "
                            "slice type");
                    }
                } else {
                    ImageRecord image;
                    image.pic_parameter_set_id = slice.pic_parameter_set_id;
                    image.frame_num = slice.frame_num;
                    image.field_pic_flag = slice.field_pic_flag;
                    image.bottom_field_flag = slice.bottom_field_flag;
                    image.idr = slice.idr;
                    image.picture_type = slice.picture_type;
                    image.start_offset_bytes = nal.start_offset;
                    image.slice_nal_offsets.push_back(nal.start_offset);
                    if (slice.picture_type == PictureType::kUnknown) {
                        image.unknown_slice_count = 1;
                        ++summary.unknown_slice_count;
                    }
                    open_pictures[key] = summary.images.size();
                    summary.images.push_back(std::move(image));
                }
                break;
            }
            default:
                ++summary.unsupported_nal_count;
                if (nal.unit_type > kNalSpsExtension) {
                    summary.diagnostics.warning(
                        nal.header_offset, "unsupported_nal_type",
                        "NAL type " + std::to_string(nal.unit_type) +
                            " is not parsed by this tool");
                }
                break;
        }
    }

    // Divide pictures into groups: a new group starts at every IDR/I picture
    // that follows another picture.  Every group records its first slice NAL
    // start-code offset, which equals the first picture's reported offset.
    for (const ImageRecord& image : summary.images) {
        const bool starts_group =
            summary.groups.empty() ||
            image.picture_type == PictureType::kI || image.idr;
        if (starts_group) {
            PictureGroup group;
            group.start_offset_bytes = image.start_offset_bytes;
            summary.groups.push_back(group);
        }
        count_image(image, summary.groups.back());
    }

    return summary;
}

}  // namespace h264
