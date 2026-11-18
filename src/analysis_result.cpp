#include "analysis_result.hpp"

namespace h264inspect {

const char* boundary_reason_name(BoundaryReason reason) {
    switch (reason) {
        case BoundaryReason::None: return "none";
        case BoundaryReason::FirstPicture: return "first_picture";
        case BoundaryReason::IdrPicture: return "idr_picture";
        case BoundaryReason::ParameterSetChange: return "parameter_set_change";
    }
    return "none";
}

}  // namespace h264inspect
