#include "fixture_builder.hpp"
#include "box_writer.hpp"

#include <filesystem>
#include <fstream>
#include <iomanip>
#include <map>
#include <sstream>

namespace mp4flow {
namespace fs = std::filesystem;

namespace {

void writeFile(const fs::path& p, const Bytes& data) {
  fs::create_directories(p.parent_path());
  std::ofstream f(p, std::ios::binary | std::ios::trunc);
  if (!f) throw Mp4Error(ErrorKind::Io, p.string(), "cannot write fixture");
  f.write(reinterpret_cast<const char*>(data.data()),
          std::streamsize(data.size()));
  if (!f) throw Mp4Error(ErrorKind::Io, p.string(), "short write fixture");
}

// Minimal visual/audio sample entry: 8-byte entry header + 24 reserved bytes.
void writeSampleEntry(BoxWriter& w, uint32_t entryType, bool encrypted) {
  w.beginBox(encrypted ? fourcc("enca") : entryType);
  w.zero(6);                // reserved
  w.u16(1);                 // data_reference_index
  w.zero(16);               // reserved
  w.u16(320);               // width
  w.u16(240);               // height
  w.u32(0x00480000);        // horiz/vert resolution 72 dpi
  w.u32(0);                 // reserved
  w.u16(1);                 // frame_count
  w.zero(32);               // compressorname + depth/pre_defined
  w.endBox();
}

Bytes buildInit(const FixturePlan& plan, bool variantB) {
  Bytes out;
  BoxWriter w(out);
  w.beginBox(fourcc("ftyp"));
  w.u32(fourcc("iso5"));
  w.u32(0x200);
  w.u32(fourcc("iso5"));
  w.u32(fourcc("mp41"));
  w.endBox();
  w.beginBox(fourcc("moov"));
  for (const FixtureTrack& tr : plan.tracks) {
    w.beginBox(fourcc("trak"));
    w.beginFull(fourcc("tkhd"), 0, 0x000007);
    w.u32(0); w.u32(0); w.u32(tr.trackId);
    w.u32(0);
    w.u32(tr.sampleDuration);
    w.zero(8);
    w.u16(0); w.u16(0);
    w.u16(0x0100);
    w.u32(0); w.u32(0); w.u32(0);
    w.u32(0x00010000); w.u32(0); w.u32(0);
    w.u32(0); w.u32(0x40000000);
    w.u32(tr.trackId << 16);
    w.u32(240 << 16);
    w.endBox();
    w.beginBox(fourcc("mdia"));
    w.beginFull(fourcc("mdhd"), 0, 0);
    w.u32(0); w.u32(0);
    uint32_t ts = tr.timescale;
    if (variantB && plan.incompatibleInit) ts = ts + 1;
    w.u32(ts);
    w.u32(tr.sampleDuration * 10);
    w.u16(0x55c4); w.u16(0);
    w.endBox();
    w.beginFull(fourcc("hdlr"), 0, 0);
    w.u32(0);
    const char* h = tr.handler.c_str();
    w.u32((uint32_t(uint8_t(h[0])) << 24) | (uint32_t(uint8_t(h[1])) << 16) |
          (uint32_t(uint8_t(h[2])) << 8) | uint32_t(uint8_t(h[3])));
    w.zero(12);
    w.u8('m'); w.u8('p'); w.u8('4'); w.u8('f'); w.u8(0);
    w.endBox();
    w.beginBox(fourcc("minf"));
    w.beginBox(fourcc("stbl"));
    w.beginFull(fourcc("stsd"), 0, 0);
    w.u32(1);
    writeSampleEntry(w, tr.sampleEntry, plan.encryptedInit);
    w.endBox(); // stsd
    w.endBox(); // stbl
    w.endBox(); // minf
    w.endBox(); // mdia
    w.endBox(); // trak
  }
  w.beginBox(fourcc("mvex"));
  for (const FixtureTrack& tr : plan.tracks) {
    w.beginFull(fourcc("trex"), 0, 0);
    w.u32(tr.trackId);
    w.u32(1);
    w.u32(tr.sampleDuration);
    w.u32(tr.sampleSize);
    w.u32(0x00010000);
    w.endBox();
  }
  w.endBox(); // mvex
  w.endBox(); // moov
  return out;
}

// Append one moof+mdat pair to `out`. Sample bytes are deterministic and
// sized exactly as declared so mdat ranges stay self-consistent.
void writeFragment(Bytes& out, const FixturePlan& plan,
                   const FixtureFragment& frag) {
  size_t moofPos = out.size();
  std::vector<size_t> trunOffsetFields;
  BoxWriter w(out);
  w.beginBox(fourcc("moof"));
  w.beginFull(fourcc("mfhd"), 0, 0);
  w.u32(frag.mfhdSeq);
  w.endBox();
  for (size_t ti = 0; ti < plan.tracks.size(); ++ti) {
    const FixtureTrack& tr = plan.tracks[ti];
    w.beginBox(fourcc("traf"));
    w.beginFull(fourcc("tfhd"), 0, 0x020000); // default-base-is-moof
    w.u32(tr.trackId);
    w.endBox();
    w.beginFull(fourcc("tfdt"), frag.tfdtVersion, 0);
    if (frag.tfdtVersion == 1) w.u64(frag.tfdt[ti]);
    else w.u32(uint32_t(frag.tfdt[ti]));
    w.endBox();
    uint32_t count = frag.sampleCounts[ti];
    uint32_t flags = 0x000001 | 0x000100 | 0x000200; // data_off,dur,size
    if (frag.cttsOffset != 0) flags |= 0x000800;
    w.beginFull(fourcc("trun"), frag.trunVersion, flags);
    w.u32(count);
    trunOffsetFields.push_back(out.size());
    w.i32(0); // data_offset placeholder, patched below
    for (uint32_t s = 0; s < count; ++s) {
      w.u32(tr.sampleDuration);
      w.u32(tr.sampleSize);
      if (frag.cttsOffset != 0) {
        if (frag.trunVersion == 1) w.i32(int32_t(s == 0 ? frag.cttsOffset : 0));
        else w.u32(uint32_t(s == 0 ? frag.cttsOffset : 0));
      }
    }
    w.endBox(); // trun
    w.endBox(); // traf
  }
  w.endBox(); // moof
  (void)moofPos;

  // One shared mdat holding all tracks' sample bytes in traf order.
  w.beginBox(fourcc("mdat"));
  uint64_t dataStartInFile = uint64_t(out.size());
  for (size_t ti = 0; ti < plan.tracks.size(); ++ti) {
    const FixtureTrack& tr = plan.tracks[ti];
    uint32_t count = frag.sampleCounts[ti];
    for (uint32_t s = 0; s < count; ++s)
      for (uint32_t b = 0; b < tr.sampleSize; ++b)
        w.u8(uint8_t((frag.mfhdSeq + tr.trackId + s + b) & 0xff));
  }
  w.endBox(); // mdat

  // Patch each trun data_offset: relative to its moof start.
  for (size_t field : trunOffsetFields) {
    int32_t rel = int32_t(dataStartInFile - moofPos) + frag.dataOffsetBias;
    out[field] = uint8_t(uint32_t(rel) >> 24);
    out[field + 1] = uint8_t(uint32_t(rel) >> 16);
    out[field + 2] = uint8_t(uint32_t(rel) >> 8);
    out[field + 3] = uint8_t(rel);
  }
}

} // namespace

FixtureLayout generateFixture(const std::string& work, const FixturePlan& plan) {
  fs::path root(work);
  fs::remove_all(root);
  fs::create_directories(root / "init");
  fs::create_directories(root / "media");
  FixtureLayout layout;
  for (int variant = 0; variant < 2; ++variant) {
    fs::path ip = root / "init" /
                  (std::string("init-") + (variant == 0 ? "a" : "b") + ".mp4");
    writeFile(ip, buildInit(plan, variant == 1));
    layout.initFiles.push_back(ip.string());
  }

  // Group fragments into files; marked joins append to the previous file.
  std::map<size_t, Bytes> fileBytes;
  std::vector<size_t> fileOfFrag;
  size_t curFile = 0;
  for (size_t i = 0; i < plan.fragments.size(); ++i) {
    bool join = std::find(plan.multiMoofJoinAt.begin(),
                          plan.multiMoofJoinAt.end(), i) !=
                plan.multiMoofJoinAt.end();
    if (!join || fileBytes.empty()) ++curFile;
    Bytes& buf = fileBytes[curFile];
    writeFragment(buf, plan, plan.fragments[i]);
    fileOfFrag.push_back(curFile);
  }
  size_t ordinal = 1;
  for (auto& [idx, buf] : fileBytes) {
    std::ostringstream name;
    name << std::setw(4) << std::setfill('0') << ordinal << '-'
         << plan.filePrefix << '-' << idx << ".m4s";
    fs::path mp = root / "media" / name.str();
    writeFile(mp, buf);
    layout.mediaFiles.push_back(mp.string());
    ++ordinal;
  }
  return layout;
}

FixturePlan makePlan(FixtureCase tc) {
  FixturePlan plan;
  plan.tracks = {
      {1, "vide", 90000, fourcc("avc1"), 90000, 64},
      {2, "soun", 48000, fourcc("mp4a"), 48000, 48},
  };
  auto f = [](uint32_t seq, uint64_t t1, uint64_t t2) {
    return FixtureFragment{seq, {t1, t2}, {2, 2}, 0, 0, 0};
  };
  if (tc == FixtureCase::Valid) {
    // 5 moofs in 4 files. Unique mfhd sequences cover 101..105; seq 105 was
    // re-delivered after a worker restart as a retry fragment whose tfdt
    // rewinds to the 104 boundary. Repacking flattens them to a continuous
    // 1..5 timeline; the largest rebase correction is one fragment step.
    plan.fragments = {
        f(101, 0, 0),
        f(102, 180000, 96000),
        f(103, 360000, 192000),
        f(104, 540000, 288000),
        f(105, 540000, 288000), // retry: duplicate delivery, tfdt rewound
    };
    // The retry moof (fragment index 3) is appended into fragment index 2's
    // file: 5 moofs delivered in 4 ordinal files.
    plan.multiMoofJoinAt = {3};
  } else if (tc == FixtureCase::Gap) {
    plan.fragments = {
        f(101, 0, 0),
        f(102, 180000, 96000),
        // 103 intentionally missing
        f(104, 360000, 192000),
        f(105, 540000, 288000),
    };
  } else if (tc == FixtureCase::BadInit) {
    plan.incompatibleInit = true;
    plan.fragments = {f(101, 0, 0), f(102, 180000, 96000)};
  } else {
    plan.encryptedInit = true;
    plan.fragments = {f(101, 0, 0)};
  }
  return plan;
}

FixtureLayout generateCase(const std::string& work, FixtureCase tc) {
  return generateFixture(work, makePlan(tc));
}

} // namespace mp4flow
