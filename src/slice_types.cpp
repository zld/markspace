#include "slice_types.hpp"

#include <cstdint>

namespace h264inspect {

const char* slice_type_name(SliceType type) {
    switch (type) {
        case SliceType::P: return "P";
        case SliceType::B: return "B";
        case SliceType::I: return "I";
        case SliceType::Sp: return "SP";
        case SliceType::Si: return "SI";
        case SliceType::Unknown: return "unknown";
    }
    return "unknown";
}

SliceType normalize_slice_type(std::uint32_t raw) {
    return raw > 9 ? SliceType::Unknown : static_cast<SliceType>(raw % 5);
}

}  // namespace h264inspect
