#include "sps_dimensions.hpp"

#include <algorithm>
#include <limits>

namespace h264inspect {

namespace {

bool mul_checked(std::uint64_t a, std::uint64_t b, std::uint32_t& out) {
    if (a != 0 && b > std::numeric_limits<std::uint64_t>::max() / a) return false;
    const std::uint64_t product = a * b;
    if (product > std::numeric_limits<std::uint32_t>::max()) return false;
    out = static_cast<std::uint32_t>(product);
    return true;
}

bool fail(Diagnostic* diagnostic, ErrorKind kind, std::uint64_t offset,
          const std::string& message) {
    if (diagnostic) {
        diagnostic->kind = kind;
        diagnostic->offset = offset;
        diagnostic->message = message;
    }
    return false;
}

}  // namespace

bool derive_sps_dimensions(Sps& sps, Diagnostic* diagnostic) {
    std::uint32_t coded_width = 0;
    std::uint32_t coded_height = 0;
    if (!mul_checked(sps.pic_width_in_mbs, 16, coded_width) ||
        !mul_checked(sps.pic_height_in_map_units,
                     sps.frame_mbs_only_flag ? 16u : 32u, coded_height)) {
        return fail(diagnostic, ErrorKind::LimitExceeded, sps.sps_byte_offset,
                    "coded picture dimensions overflow 32 bits");
    }
    if (coded_width == 0 || coded_height == 0) {
        return fail(diagnostic, ErrorKind::IllegalValue, sps.sps_byte_offset,
                    "coded picture dimensions are zero");
    }
    if (coded_width > kMaxDimension || coded_height > kMaxDimension) {
        return fail(diagnostic, ErrorKind::LimitExceeded, sps.sps_byte_offset,
                    "coded picture " + std::to_string(coded_width) + "x" +
                        std::to_string(coded_height) + " exceeds limit " +
                        std::to_string(kMaxDimension));
    }
    sps.coded_width = coded_width;
    sps.coded_height = coded_height;

    // Crop offsets are expressed in chroma sample units, so they scale by
    // SubWidthC/SubHeightC; an interlaced frame doubles the vertical unit.
    std::uint32_t crop_unit_x = 1;
    std::uint32_t crop_unit_y = sps.frame_mbs_only_flag ? 1u : 2u;
    if (sps.ChromaArrayType() != 0) {
        crop_unit_x = sps.SubWidthC();
        crop_unit_y = sps.SubHeightC() * (sps.frame_mbs_only_flag ? 1u : 2u);
    }

    const std::uint64_t horizontal_crop =
        static_cast<std::uint64_t>(sps.crop_left) + sps.crop_right;
    const std::uint64_t vertical_crop =
        static_cast<std::uint64_t>(sps.crop_top) + sps.crop_bottom;
    const std::uint64_t cropped_width =
        coded_width - std::min<std::uint64_t>(coded_width, horizontal_crop * crop_unit_x);
    const std::uint64_t cropped_height =
        coded_height - std::min<std::uint64_t>(coded_height, vertical_crop * crop_unit_y);

    if (horizontal_crop * crop_unit_x >= coded_width ||
        vertical_crop * crop_unit_y >= coded_height ||
        cropped_width == 0 || cropped_height == 0) {
        return fail(diagnostic, ErrorKind::IllegalValue, sps.sps_byte_offset,
                    "frame cropping removes the entire picture");
    }
    if (cropped_width > kMaxDimension || cropped_height > kMaxDimension) {
        return fail(diagnostic, ErrorKind::LimitExceeded, sps.sps_byte_offset,
                    "cropped picture exceeds dimension limit");
    }
    sps.width = static_cast<std::uint32_t>(cropped_width);
    sps.height = static_cast<std::uint32_t>(cropped_height);
    return true;
}

}  // namespace h264inspect
