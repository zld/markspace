#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "frame_buffer.hpp"
#include "timeline.hpp"
#include "transition.hpp"

namespace tl {

struct RenderConfig {
  Rational fps{30, 1};
  Rational duration{1, 1};
  int width = 0;
  int height = 0;
  std::vector<TransitionType> transitions;
  std::vector<SpeedPiece> speed_pieces;  // empty -> uniform 1x
  uint32_t seed = 1337;
  int jobs = 1;
  std::string out_dir;
};

struct TypeStats {
  TransitionType type = TransitionType::Fade;
  int64_t frames = 0;
  uint64_t first_hash = 0;
  uint64_t last_hash = 0;
  long long mid_changed_pixels = 0;        // mean over mid frames, rounded
  long long mid_mean_abs_delta_milli = 0;  // mean over mid frames, *1000
  std::vector<uint64_t> frame_hashes;
};

// Renders one transition sequence: biome keyframe 0 -> keyframe 1 through
// the shared transition kernel, scheduled by the Timeline. Deterministic
// for any jobs >= 1. When write_files is set, frames land in cfg.out_dir
// as "<type>_frame_%03d.ppm" via atomic writes.
TypeStats render_sequence(const RenderConfig& cfg, TransitionType type,
                          bool write_files);

std::string frame_filename(TransitionType type, int64_t index);
std::string hash_hex(uint64_t h);  // 16 lowercase hex digits
std::string format_stats_line(const TypeStats& s);

// Manifest: round-tripable render record used by `verify`.
void write_manifest(const std::string& path, const RenderConfig& cfg,
                    const std::vector<TypeStats>& stats);
struct Manifest {
  RenderConfig cfg;
  std::vector<TypeStats> stats;
};
Manifest read_manifest(const std::string& path);

}  // namespace tl
