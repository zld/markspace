#include "nal_parser.hpp"

namespace h264inspect {

const char* nal_unit_type_name(int type) {
    switch (type) {
        case 0: return "unspecified";
        case 1: return "non_idr_slice";
        case 2: return "slice_data_partition_a";
        case 3: return "slice_data_partition_b";
        case 4: return "slice_data_partition_c";
        case 5: return "idr_slice";
        case 6: return "sei";
        case 7: return "sps";
        case 8: return "pps";
        case 9: return "access_unit_delimiter";
        case 10: return "end_of_sequence";
        case 11: return "end_of_stream";
        case 12: return "filler_data";
        case 13: return "sps_extension";
        case 14: return "prefix_nal_unit";
        case 15: return "subset_sps";
        case 16: return "reserved_16";
        case 17: return "reserved_17";
        case 18: return "reserved_18";
        case 19: return "auxiliary_slice";
        case 20: return "slice_extension";
        case 21: return "slice_extension_depth";
        default:
            if (type >= 22 && type <= 31) return "reserved";
            return "unknown";
    }
}

}  // namespace h264inspect
