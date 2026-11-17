#include "renderer.hpp"

#include <cinttypes>
#include <cstdio>
#include <filesystem>
#include <vector>

#include "frame_buffer.hpp"
#include "generators.hpp"
#include "hash.hpp"
#include "ppm_writer.hpp"
#include "timeline.hpp"
#include "transition_kernel.hpp"

namespace tl {

namespace {

struct FrameDiff {
  int64_t changed = 0;
  double mae = 0.0;
};

FrameDiff diff_frames(const FrameBuffer& a, const FrameBuffer& b) {
  FrameDiff d;
  int64_t sum = 0;
  const size_t n = a.size();
  for (size_t i = 0; i < n; ++i) {
    const RGB& pa = a.data()[i];
    const RGB& pb = b.data()[i];
    if (pa != pb) {
      ++d.changed;
      sum += std::abs(static_cast<int>(pa.r) - pb.r) +
             std::abs(static_cast<int>(pa.g) - pb.g) +
             std::abs(static_cast<int>(pa.b) - pb.b);
    }
  }
  d.mae = static_cast<double>(sum) / (static_cast<double>(n) * 3.0);
  return d;
}

std::string frame_path(const std::string& dir, int64_t f) {
  char name[64];
  std::snprintf(name, sizeof name, "frame_%06" PRId64 ".ppm", f);
  return dir + "/" + name;
}

uint64_t frame_hash(const FrameBuffer& fb) {
  return fnv1a64(fb.data(), fb.size() * sizeof(RGB));
}

// Renders output frame f into `out`, reusing fa/fb as scratch.
void render_frame(const Timeline& tl, int64_t f, FrameBuffer& fa,
                  FrameBuffer& fb, FrameBuffer& out, FrameSource& fs) {
  fs = tl.map(f);
  const Manifest& m = tl.manifest();
  generate_frame(m.clips[fs.clip_a].gen, fs.src_a, fa);
  if (!fs.in_transition) {
    out = fa;
    return;
  }
  generate_frame(m.clips[fs.clip_b].gen, fs.src_b, fb);
  const KernelType k = m.transitions[fs.transition_index].kernel;
  const uint32_t w = out.width(), h = out.height();
  for (uint32_t y = 0; y < h; ++y)
    for (uint32_t x = 0; x < w; ++x)
      out.at(x, y) =
          kernel_blend(k, fa.at(x, y), fb.at(x, y), x, y, w, h, fs.progress);
}

}  // namespace

int run_render(const Manifest& m, const RenderOptions& opt) {
  Timeline tl(m);  // throws TimelineError; caller maps it to exit 3

  std::error_code ec;
  std::filesystem::create_directories(opt.out_dir, ec);
  if (ec) {
    std::fprintf(stderr, "error: cannot create output dir %s: %s\n",
                 opt.out_dir.c_str(), ec.message().c_str());
    return 4;
  }

  const int64_t total = tl.total_frames();
  const Manifest& man = tl.manifest();
  FrameBuffer fa(man.width, man.height), fb(man.width, man.height),
      out(man.width, man.height), prev(man.width, man.height);
  FrameSource fs;
  std::string err;

  std::vector<uint64_t> hashes(static_cast<size_t>(total));
  const size_t ntrans = man.transitions.size();
  std::vector<std::vector<FrameDiff>> middle(ntrans);

  for (int64_t f = 0; f < total; ++f) {
    render_frame(tl, f, fa, fb, out, fs);
    hashes[static_cast<size_t>(f)] = frame_hash(out);
    if (fs.in_transition && fs.transition_frame > 0) {
      FrameDiff d = diff_frames(prev, out);
      if (fs.transition_frame < man.transitions[fs.transition_index].frames - 1)
        middle[fs.transition_index].push_back(d);
    }
    if (!write_ppm(out, frame_path(opt.out_dir, f), err)) {
      std::fprintf(stderr, "error: %s\n", err.c_str());
      return 4;
    }
    prev = out;
  }

  for (size_t t = 0; t < ntrans; ++t) {
    int64_t start = tl.transition_start(static_cast<int>(t));
    int64_t frames = man.transitions[t].frames;
    std::printf("transition %zu %s frames=%" PRId64 " first_hash=%016llx last_hash=%016llx\n",
                t, to_string(man.transitions[t].kernel), frames,
                (unsigned long long)hashes[start],
                (unsigned long long)hashes[start + frames - 1]);
    for (size_t i = 0; i < middle[t].size(); ++i)
      std::printf("  frame=%" PRId64 " middle_changed_pixels=%" PRId64
                  " middle_mae=%.3f\n",
                  start + 1 + (int64_t)i, middle[t][i].changed,
                  middle[t][i].mae);
  }
  std::printf("rendered %" PRId64 " frames -> %s\n", total,
              opt.out_dir.c_str());

  if (!opt.verify) return 0;

  // Consistency pass: re-render every frame, compare hashes, then re-read
  // the PPM from disk and check header and pixels. First mismatch wins.
  for (int64_t f = 0; f < total; ++f) {
    render_frame(tl, f, fa, fb, out, fs);
    uint64_t h = frame_hash(out);
    if (h != hashes[static_cast<size_t>(f)]) {
      std::fprintf(stderr,
                   "verify: frame %" PRId64 " hash mismatch: %016llx != %016llx\n",
                   f, (unsigned long long)h,
                   (unsigned long long)hashes[static_cast<size_t>(f)]);
      return 5;
    }
    if (!verify_ppm(out, frame_path(opt.out_dir, f), err)) {
      std::fprintf(stderr, "verify: frame %" PRId64 " read-back failed: %s\n",
                   f, err.c_str());
      return 5;
    }
  }
  std::printf("verify: ok (%" PRId64
              " frames re-rendered, hashes and PPM read-back match)\n",
              total);
  return 0;
}

}  // namespace tl
