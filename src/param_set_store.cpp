#include "param_set_store.hpp"

#include <utility>

namespace h264inspect {

ParamSetImage make_param_set_image(const std::uint8_t* payload, std::size_t size,
                                   std::uint64_t offset) {
    ParamSetImage image;
    RbspBuffer rbsp;
    Diagnostic diag;
    if (!build_rbsp(payload, size, offset, rbsp, &diag)) return image;
    image.rbsp = std::move(rbsp.bytes);
    image.valid = true;
    return image;
}

const Sps* ParamSetStore::find_sps(std::uint32_t id) const {
    auto it = sps_.find(id);
    return it == sps_.end() ? nullptr : &it->second.sps;
}

const Pps* ParamSetStore::find_pps(std::uint32_t id) const {
    auto it = pps_.find(id);
    return it == pps_.end() ? nullptr : &it->second.pps;
}

std::uint32_t ParamSetStore::sps_revision(std::uint32_t id) const {
    auto it = sps_.find(id);
    return it == sps_.end() ? 0 : it->second.revision;
}

std::uint32_t ParamSetStore::pps_revision(std::uint32_t id) const {
    auto it = pps_.find(id);
    return it == pps_.end() ? 0 : it->second.revision;
}

bool ParamSetStore::store_sps(const Sps& value, const ParamSetImage& image) {
    auto it = sps_.find(value.id);
    if (it != sps_.end() && it->second.image == image) return false;
    SpsEntry entry;
    entry.sps = value;
    entry.image = image;
    entry.revision = ++revision_counter_;
    sps_[value.id] = std::move(entry);
    return true;
}

bool ParamSetStore::store_pps(const Pps& value, const ParamSetImage& image) {
    auto it = pps_.find(value.id);
    if (it != pps_.end() && it->second.image == image) return false;
    PpsEntry entry;
    entry.pps = value;
    entry.image = image;
    entry.revision = ++revision_counter_;
    pps_[value.id] = std::move(entry);
    return true;
}

}  // namespace h264inspect
