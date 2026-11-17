#include "render_pipeline.hpp"

#include <atomic>
#include <cstdio>
#include <exception>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <thread>

#include "biome.hpp"
#include "errors.hpp"
#include "ppm.hpp"

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

TypeStats render_sequence(const RenderConfig& cfg, TransitionType type,
                          bool write_files) {
  const int64_t n = frames_for_duration(cfg.duration, cfg.fps);
  std::vector<SpeedPiece> pieces = cfg.speed_pieces;
  if (pieces.empty()) pieces.push_back(SpeedPiece{0, n, Rational{1, 1}});
  Timeline timeline(cfg.fps, n, pieces);

  const __int128 total_bytes =
      static_cast<__int128>(n) * cfg.width * cfg.height * 3;
  if (total_bytes > kMaxTotalBytes)
    throw ArgError("sequence of " + std::to_string(n) + " frames at " +
                   std::to_string(cfg.width) + "x" + std::to_string(cfg.height) +
                   " exceeds buffered byte limit");

  const FrameBuffer from = make_biome_frame(cfg.seed, cfg.width, cfg.height, 0);
  const FrameBuffer to = make_biome_frame(cfg.seed, cfg.width, cfg.height, 1);
  std::vector<FrameBuffer> frames(static_cast<size_t>(n));
  for (auto& f : frames) f = FrameBuffer(cfg.width, cfg.height);

  // Parallel render; every frame is independent, so output is identical
  // for any job count. Exceptions are rethrown on the calling thread.
  const int jobs = cfg.jobs < 1 ? 1 : (cfg.jobs > 64 ? 64 : cfg.jobs);
  std::atomic<int64_t> next{0};
  std::exception_ptr failure;
  std::mutex failure_mu;
  auto worker = [&] {
    try {
      for (;;) {
        const int64_t i = next.fetch_add(1);
        if (i >= n) break;
        const Rational p = timeline.progress(i);
        apply_transition(type, from, to, p.num, p.den,
                         frames[static_cast<size_t>(i)]);
      }
    } catch (...) {
      std::lock_guard<std::mutex> lk(failure_mu);
      if (!failure) failure = std::current_exception();
    }
  };
  {
    std::vector<std::thread> pool;
    for (int j = 0; j < jobs; ++j) pool.emplace_back(worker);
    for (auto& t : pool) t.join();
  }
  if (failure) std::rethrow_exception(failure);

  if (write_files) {
    std::filesystem::create_directories(cfg.out_dir);
    for (int64_t i = 0; i < n; ++i)
      write_ppm_atomic(cfg.out_dir + "/" + frame_filename(type, i),
                       frames[static_cast<size_t>(i)]);
  }

  TypeStats st;
  st.type = type;
  st.frames = n;
  st.frame_hashes.resize(static_cast<size_t>(n));
  for (int64_t i = 0; i < n; ++i)
    st.frame_hashes[static_cast<size_t>(i)] =
        hash_frame(frames[static_cast<size_t>(i)]);
  st.first_hash = st.frame_hashes.front();
  st.last_hash = st.frame_hashes.back();
  long long changed_sum = 0, delta_sum = 0, count = 0;
  for (int64_t i = 1; i + 1 < n; ++i) {  // mid frames only
    const FrameDiff d = diff_frames(frames[static_cast<size_t>(i)],
                                    frames[static_cast<size_t>(i - 1)]);
    changed_sum += d.changed_pixels;
    delta_sum += d.mean_abs_delta_milli;
    ++count;
  }
  if (count > 0) {
    st.mid_changed_pixels = (changed_sum + count / 2) / count;
    st.mid_mean_abs_delta_milli = (delta_sum + count / 2) / count;
  }
  return st;
}

void write_manifest(const std::string& path, const RenderConfig& cfg,
                    const std::vector<TypeStats>& stats) {
  const std::string tmp = path + ".tmp";
  {
    std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
    if (!out) throw IoError("cannot open temp file '" + tmp + "' for writing");
    out << "transition-lab-manifest v1\n";
    out << "seed " << cfg.seed << "\n";
    out << "fps " << cfg.fps.num << "/" << cfg.fps.den << "\n";
    out << "duration " << cfg.duration.num << "/" << cfg.duration.den << "\n";
    out << "size " << cfg.width << "x" << cfg.height << "\n";
    if (cfg.speed_pieces.empty()) {
      out << "speeds -\n";
    } else {
      out << "speeds";
      for (const SpeedPiece& p : cfg.speed_pieces)
        out << " " << p.begin_frame << ":" << p.end_frame << ":"
            << p.speed.num << "/" << p.speed.den;
      out << "\n";
    }
    for (const TypeStats& s : stats) {
      out << "type " << transition_name(s.type) << " frames " << s.frames
          << " first_hash " << hash_hex(s.first_hash) << " last_hash "
          << hash_hex(s.last_hash) << " mid_changed_pixels "
          << s.mid_changed_pixels << " mid_mean_abs_delta_milli "
          << s.mid_mean_abs_delta_milli << "\n";
      for (int64_t i = 0; i < s.frames; ++i)
        out << "frame " << transition_name(s.type) << " " << i << " "
            << hash_hex(s.frame_hashes[static_cast<size_t>(i)]) << "\n";
    }
    if (!out) {
      out.close();
      std::remove(tmp.c_str());
      throw IoError("failed writing temp file '" + tmp + "'");
    }
  }
  if (std::rename(tmp.c_str(), path.c_str()) != 0) {
    std::remove(tmp.c_str());
    throw IoError("cannot rename '" + tmp + "' -> '" + path + "'");
  }
}

namespace {

uint64_t parse_hash(const std::string& tok, const std::string& where) {
  if (tok.size() != 16)
    throw DataError("manifest: bad hash '" + tok + "' at " + where);
  uint64_t v = 0;
  for (char c : tok) {
    v <<= 4;
    if (c >= '0' && c <= '9') v |= static_cast<uint64_t>(c - '0');
    else if (c >= 'a' && c <= 'f') v |= static_cast<uint64_t>(c - 'a' + 10);
    else throw DataError("manifest: bad hash '" + tok + "' at " + where);
  }
  return v;
}

int64_t parse_i64(const std::string& tok, const std::string& where) {
  size_t used = 0;
  long long v = 0;
  try {
    v = std::stoll(tok, &used);
  } catch (...) {
    throw DataError("manifest: bad number '" + tok + "' at " + where);
  }
  if (used != tok.size())
    throw DataError("manifest: bad number '" + tok + "' at " + where);
  return v;
}

TypeStats* find_stats(Manifest& m, TransitionType t) {
  for (auto& s : m.stats)
    if (s.type == t) return &s;
  return nullptr;
}

}  // namespace

Manifest read_manifest(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  if (!in) throw IoError("cannot open manifest '" + path + "'");
  Manifest m;
  m.cfg.out_dir = std::filesystem::path(path).parent_path().string();
  std::string line;
  int lineno = 0;
  bool header_seen = false;
  while (std::getline(in, line)) {
    ++lineno;
    const std::string where = path + ":" + std::to_string(lineno);
    if (!line.empty() && line.back() == '\r') line.pop_back();
    if (line.empty()) continue;
    std::istringstream ls(line);
    std::string key;
    ls >> key;
    if (!header_seen) {
      if (line != "transition-lab-manifest v1")
        throw DataError("manifest: bad header at " + where);
      header_seen = true;
      continue;
    }
    if (key == "seed") {
      std::string tok;
      if (!(ls >> tok)) throw DataError("manifest: missing seed at " + where);
      m.cfg.seed = static_cast<uint32_t>(parse_i64(tok, where));
    } else if (key == "fps" || key == "duration") {
      std::string tok;
      if (!(ls >> tok)) throw DataError("manifest: missing value at " + where);
      const Rational r = parse_rational(tok, key.c_str());
      (key == "fps" ? m.cfg.fps : m.cfg.duration) = r;
    } else if (key == "size") {
      std::string tok;
      if (!(ls >> tok)) throw DataError("manifest: missing size at " + where);
      const size_t x = tok.find('x');
      if (x == std::string::npos)
        throw DataError("manifest: bad size '" + tok + "' at " + where);
      m.cfg.width = static_cast<int>(parse_i64(tok.substr(0, x), where));
      m.cfg.height = static_cast<int>(parse_i64(tok.substr(x + 1), where));
    } else if (key == "speeds") {
      std::string tok;
      while (ls >> tok) {
        if (tok == "-") continue;
        const size_t c1 = tok.find(':');
        const size_t c2 = tok.find(':', c1 == std::string::npos ? c1 : c1 + 1);
        if (c1 == std::string::npos || c2 == std::string::npos)
          throw DataError("manifest: bad speed piece '" + tok + "' at " + where);
        SpeedPiece p;
        p.begin_frame = parse_i64(tok.substr(0, c1), where);
        p.end_frame = parse_i64(tok.substr(c1 + 1, c2 - c1 - 1), where);
        p.speed = parse_rational(tok.substr(c2 + 1), "speed");
        m.cfg.speed_pieces.push_back(p);
      }
    } else if (key == "type") {
      std::string name;
      if (!(ls >> name)) throw DataError("manifest: bad type at " + where);
      TransitionType t;
      if (!parse_transition(name, t))
        throw DataError("manifest: unknown type '" + name + "' at " + where);
      TypeStats s;
      s.type = t;
      std::string k, tok;
      while (ls >> k >> tok) {
        if (k == "frames") s.frames = parse_i64(tok, where);
        else if (k == "first_hash") s.first_hash = parse_hash(tok, where);
        else if (k == "last_hash") s.last_hash = parse_hash(tok, where);
        else if (k == "mid_changed_pixels")
          s.mid_changed_pixels = parse_i64(tok, where);
        else if (k == "mid_mean_abs_delta_milli")
          s.mid_mean_abs_delta_milli = parse_i64(tok, where);
        else throw DataError("manifest: bad key '" + k + "' at " + where);
      }
      if (find_stats(m, t))
        throw DataError("manifest: duplicate type '" + name + "' at " + where);
      s.frame_hashes.assign(static_cast<size_t>(s.frames), 0);
      m.cfg.transitions.push_back(t);
      m.stats.push_back(s);
    } else if (key == "frame") {
      std::string name, idx, hex;
      if (!(ls >> name >> idx >> hex))
        throw DataError("manifest: bad frame line at " + where);
      TransitionType t;
      if (!parse_transition(name, t))
        throw DataError("manifest: unknown type '" + name + "' at " + where);
      TypeStats* s = find_stats(m, t);
      if (!s) throw DataError("manifest: frame before type at " + where);
      const int64_t i = parse_i64(idx, where);
      if (i < 0 || i >= s->frames)
        throw DataError("manifest: frame index out of range at " + where);
      s->frame_hashes[static_cast<size_t>(i)] = parse_hash(hex, where);
    } else {
      throw DataError("manifest: unknown key '" + key + "' at " + where);
    }
  }
  if (!header_seen) throw DataError("manifest: empty file '" + path + "'");
  if (m.stats.empty()) throw DataError("manifest: no transitions recorded");
  return m;
}

}  // namespace tl
