#include "render_pipeline.hpp"

#include <cstdio>

#include "errors.hpp"

namespace tl {

std::string frame_filename(TransitionType type, int64_t index) {
  char buf[64];
  std::snprintf(buf, sizeof(buf), "%s_frame_%03lld.ppm",
                transition_name(type), static_cast<long long>(index));
  return buf;
}

std::string hash_hex(uint64_t h) {
  char buf[17];
  std::snprintf(buf, sizeof(buf), "%016llx",
                static_cast<unsigned long long>(h));
  return buf;
}

std::string format_stats_line(const TypeStats& s) {
  char buf[256];
  std::snprintf(buf, sizeof(buf),
                "type=%s frames=%lld first_hash=%s last_hash=%s "
                "mid_changed_pixels=%lld mid_mean_abs_delta=%lld.%02lld",
                transition_name(s.type), static_cast<long long>(s.frames),
                hash_hex(s.first_hash).c_str(), hash_hex(s.last_hash).c_str(),
                s.mid_changed_pixels, s.mid_mean_abs_delta_milli / 1000,
                (s.mid_mean_abs_delta_milli % 1000) / 10);
  return buf;
}

TypeStats render_sequence(const RenderConfig& /*cfg*/, TransitionType type,
                          bool /*write_files*/) {
  throw ArgError(std::string("render_sequence: not implemented (") +
                 transition_name(type) + ")");
}

void write_manifest(const std::string& path, const RenderConfig& /*cfg*/,
                    const std::vector<TypeStats>& /*stats*/) {
  throw ArgError("write_manifest: not implemented (" + path + ")");
}

Manifest read_manifest(const std::string& path) {
  throw ArgError("read_manifest: not implemented (" + path + ")");
}

}  // namespace tl
