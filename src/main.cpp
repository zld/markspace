#include <cstdlib>
#include <exception>
#include <iomanip>
#include <iostream>
#include <sstream>
#include <string>
#include <vector>

#include "errors.hpp"
#include "frame_buffer.hpp"
#include "hash.hpp"
#include "manifest.hpp"
#include "renderer.hpp"
#include "sample.hpp"
#include "timeline.hpp"

namespace tr {
namespace {

struct Args {
  std::string command;
  std::string out;
  int width = 160;
  int height = 90;
  Rational fps = Rational(30, 1);
  std::string sample = "standard";
};

Rational parseRational(const std::string& s, const std::string& flag) {
  const auto slash = s.find('/');
  try {
    if (slash == std::string::npos) {
      return Rational(std::stoll(s), 1);
    }
    return Rational(std::stoll(s.substr(0, slash)),
                    std::stoll(s.substr(slash + 1)));
  } catch (const std::exception&) {
    throw UsageError("bad rational for " + flag + ": " + s);
  }
}

Args parseArgs(int argc, char** argv) {
  Args a;
  if (argc < 2) throw UsageError("missing command (demo|verify|selftest)");
  a.command = argv[1];
  if (a.command != "demo" && a.command != "verify" &&
      a.command != "selftest") {
    throw UsageError("unknown command: " + a.command);
  }
  for (int i = 2; i < argc; ++i) {
    const std::string f = argv[i];
    auto need = [&](const std::string& flag) {
      if (i + 1 >= argc) throw UsageError("flag " + flag + " needs a value");
      return std::string(argv[++i]);
    };
    if (f == "--out") a.out = need(f);
    else if (f == "--width") a.width = std::stoi(need(f));
    else if (f == "--height") a.height = std::stoi(need(f));
    else if (f == "--fps") a.fps = parseRational(need(f), f);
    else if (f == "--sample") a.sample = need(f);
    else throw UsageError("unknown flag: " + f);
  }
  if (a.out.empty()) {
    throw UsageError("--out <directory> is required");
  }
  if (!a.fps.isPositive()) {
    throw UsageError("--fps must be positive, got " + a.fps.str());
  }
  return a;
}

std::string framePath(const std::string& dir, const std::string& type,
                      int index) {
  std::ostringstream os;
  os << dir << '/' << type << '_' << std::setw(6) << std::setfill('0')
     << index << ".ppm";
  return os.str();
}

void printStats(const std::string& type, int frames,
                const std::string& firstHash, const std::string& lastHash,
                const FrameDiff& mid, int width, int height) {
  // Reason field explains any zeroed statistics explicitly.
  std::string reason = "ok";
  FrameDiff shown = mid;
  if (frames < 3) {
    reason = "insufficient_samples";
    shown = FrameDiff{0, 0};
  } else if (mid.diffPixels == 0) {
    reason = "identical_mid_frames";
  }
  std::cout << "type=" << type << " frames=" << frames
            << " first_hash=" << firstHash << " last_hash=" << lastHash
            << " mid_diff_pixels=" << shown.diffPixels
            << " mid_mean_abs_diff="
            << formatMeanAbs(shown, width, height)
            << " reason=" << reason << '\n';
}

int runDemo(const Args& a) {
  // All timeline validation happens before any file is created.
  std::vector<SampleScene> scenes =
      buildSample(a.sample, a.width, a.height, a.fps);
  ensureOutputDir(a.out);
  checkOutputDirSafe(a.out, kManifestName);

  Manifest manifest{a.width, a.height, a.fps.num(), a.fps.den(), a.sample, {}};
  FrameBuffer fb(a.width, a.height);
  std::vector<FrameBuffer> midFrames;
  midFrames.reserve(2);

  for (const SampleScene& sc : scenes) {
    const Renderer renderer(sc.timeline);
    const std::string type = transitionName(sc.type);
    const int frames = static_cast<int>(sc.timeline.totalFrames);
    std::string firstHash, lastHash;
    midFrames.clear();
    for (int g = 0; g < frames; ++g) {
      renderer.renderFrame(g, fb);
      if (g == 0) firstHash = toHex64(hashFrame(fb));
      if (g == frames / 2 - 1 || g == frames / 2) midFrames.push_back(fb);
      if (g == frames - 1) lastHash = toHex64(hashFrame(fb));
      // Atomic per-frame write; a failure keeps earlier completed frames.
      writePpmAtomic(framePath(a.out, type, g), fb);
    }
    const FrameDiff mid =
        midFrames.size() == 2 ? compareFrames(midFrames[0], midFrames[1])
                              : FrameDiff{0, 0};
    printStats(type, frames, firstHash, lastHash, mid, a.width, a.height);
    manifest.entries.push_back(
        ManifestEntry{type, frames, firstHash, lastHash});
  }
  writeManifest(a.out, manifest);
  std::cout << "demo complete: " << a.out << '\n';
  return 0;
}

int runVerify(const Args& a) {
  const Manifest m = readManifest(a.out);
  const Rational fps(m.fpsNum, m.fpsDen);
  const std::vector<SampleScene> scenes =
      buildSample(m.sample, m.width, m.height, fps);
  FrameBuffer rendered(m.width, m.height);
  int failures = 0;
  for (const SampleScene& sc : scenes) {
    const Renderer renderer(sc.timeline);
    const std::string type = transitionName(sc.type);
    const ManifestEntry* expected = nullptr;
    for (const ManifestEntry& e : m.entries) {
      if (e.type == type) expected = &e;
    }
    if (expected == nullptr) {
      std::cerr << "verify: manifest missing scene " << type << '\n';
      return 3;
    }
    for (int g = 0; g < expected->frames; ++g) {
      renderer.renderFrame(g, rendered);
      FrameBuffer onDisk(m.width, m.height);
      try {
        onDisk = readPpm(framePath(a.out, type, g));
      } catch (const IoError& e) {
        // A frame that cannot be parsed back means the stored pixels cannot
        // be validated: a verification failure, not a runtime render fault.
        std::cerr << "verify: unreadable frame " << type << " frame " << g
                  << ": " << e.what() << '\n';
        ++failures;
        continue;
      }
      if (toHex64(hashFrame(rendered)) != toHex64(hashFrame(onDisk))) {
        std::cerr << "verify: pixel mismatch " << type << " frame " << g
                  << '\n';
        ++failures;
      }
    }
  }
  if (failures > 0) {
    std::cerr << "verify failed: " << failures << " mismatching frame(s)\n";
    return 4;
  }
  std::cout << "verify ok: all frames reproduce identical pixels and hashes\n";
  return 0;
}

}  // namespace
}  // namespace tr

int main(int argc, char** argv) {
  using namespace tr;
  try {
    const Args a = parseArgs(argc, argv);
    if (a.command == "selftest") {
      std::cerr << "selftest is provided by the transition_selftest binary\n";
      return 2;
    }
    return a.command == "demo" ? runDemo(a) : runVerify(a);
  } catch (const UsageError& e) {
    std::cerr << "error: " << e.what() << '\n';
    return 2;
  } catch (const RenderError& e) {
    std::cerr << "error: " << e.what() << '\n';
    return 3;
  } catch (const std::exception& e) {
    std::cerr << "fatal: " << e.what() << '\n';
    return 3;
  }
}
