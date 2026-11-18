// Active parameter-set store with content revisions.
#pragma once

#include <cstdint>
#include <map>
#include <vector>

#include "bit_reader.hpp"
#include "pps_parser.hpp"
#include "sps_types.hpp"

namespace h264inspect {

// RBSP image of a parameter set, so an identical re-definition is not mistaken
// for a switch while genuinely different content is detected.
struct ParamSetImage {
    std::vector<std::uint8_t> rbsp;
    bool valid = false;

    bool operator==(const ParamSetImage& other) const {
        return valid == other.valid && rbsp == other.rbsp;
    }
    bool operator!=(const ParamSetImage& other) const { return !(*this == other); }
};

// Builds the RBSP image of a NAL payload for change detection.
ParamSetImage make_param_set_image(const std::uint8_t* payload, std::size_t size,
                                   std::uint64_t offset);

class ParamSetStore {
public:
    // Returned pointers stay valid until the next store call.
    const Sps* find_sps(std::uint32_t id) const;
    const Pps* find_pps(std::uint32_t id) const;

    // Revision 0 means "never stored". Revisions are globally ordered so that
    // two parameter sets can be compared for identity of definition.
    std::uint32_t sps_revision(std::uint32_t id) const;
    std::uint32_t pps_revision(std::uint32_t id) const;

    // Stores a definition. Returns true when the stored content changed
    // (including the first definition), false when it is a repeat.
    bool store_sps(const Sps& value, const ParamSetImage& image);
    bool store_pps(const Pps& value, const ParamSetImage& image);

private:
    struct SpsEntry {
        Sps sps;
        ParamSetImage image;
        std::uint32_t revision = 0;
    };
    struct PpsEntry {
        Pps pps;
        ParamSetImage image;
        std::uint32_t revision = 0;
    };

    std::map<std::uint32_t, SpsEntry> sps_;
    std::map<std::uint32_t, PpsEntry> pps_;
    std::uint32_t revision_counter_ = 0;
};

}  // namespace h264inspect
