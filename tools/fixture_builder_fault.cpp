#include "fixture_builder.hpp"

#include <algorithm>
#include <cstring>

namespace mp4::fix {

namespace {

void put32(std::vector<uint8_t>& o, uint32_t v) {
    o.push_back(uint8_t(v >> 24));
    o.push_back(uint8_t(v >> 16));
    o.push_back(uint8_t(v >> 8));
    o.push_back(uint8_t(v));
}
void put_fcc(std::vector<uint8_t>& o, const char* t) {
    o.insert(o.end(), t, t + 4);
}
void put_box(std::vector<uint8_t>& o, const char* type,
             const std::vector<uint8_t>& payload) {
    put32(o, uint32_t(8 + payload.size()));
    put_fcc(o, type);
    o.insert(o.end(), payload.begin(), payload.end());
}

}  // namespace

std::vector<std::pair<std::string, std::vector<uint8_t>>>
build_fault_set(Fault fault, const SampleSpec& spec) {
    auto normal = build_normal_set(spec);  // names seg-01..seg-06
    auto named = [&](const std::string& n, std::vector<uint8_t> b) {
        return std::make_pair(n, std::move(b));
    };

    switch (fault) {
        case Fault::Missing:
            normal.erase(std::remove_if(normal.begin(), normal.end(),
                                        [](const auto& p) {
                                            return p.first == "seg-04.m4s";
                                        }),
                         normal.end());
            return normal;

        case Fault::Duplicate: {
            // seg-03 and a re-delivered copy both carry sequence 3.
            auto out = normal;
            out.insert(out.begin() + 3,
                       named("seg-03-redelivered.m4s", normal[2].second));
            return out;
        }

        case Fault::OutOfOrder: {
            // Keep every moof's original sequence but arrange the sorted file
            // order to 1,2,4,3,5,6, so the fragments arrive out of order.
            auto out = normal;
            std::swap(out[2].first, out[3].first);
            // After swapping names, also swap the byte payloads back so each
            // file name position carries the mismatched sequence: the file
            // now named seg-03 holds sequence 4 bytes and vice versa.
            std::swap(out[2].second, out[3].second);
            return out;
        }

        case Fault::Length: {
            // Shorten the mdat payload of seg-02 by four bytes and fix its
            // size field, so trun-declared sample sizes exceed the payload.
            auto out = normal;
            auto& bytes = out[1].second;
            size_t mdat = bytes.size();
            for (size_t i = bytes.size() >= 8 ? bytes.size() - 8 : 0;;
                 --i) {
                if (i + 4 <= bytes.size() &&
                    std::memcmp(bytes.data() + i, "mdat", 4) == 0) {
                    mdat = i - 4;  // size field precedes the type
                    break;
                }
                if (i == 0) break;
            }
            bytes.resize(bytes.size() - 4);
            uint32_t mdat_size = uint32_t(bytes.size() - mdat);
            bytes[mdat] = uint8_t(mdat_size >> 24);
            bytes[mdat + 1] = uint8_t(mdat_size >> 16);
            bytes[mdat + 2] = uint8_t(mdat_size >> 8);
            bytes[mdat + 3] = uint8_t(mdat_size);
            return out;
        }

        case Fault::Drift: {
            // seg-02 carries explicit tfhd defaults. Patch its
            // default_sample_duration in place (same field width) so the
            // fragment default conflicts with the trex value in the init
            // segment; compatibility must flag the drift and block writing.
            auto out = normal;
            auto& bytes = out[1].second;
            for (size_t i = 0; i + 4 <= bytes.size(); ++i) {
                if (std::memcmp(bytes.data() + i, "tfhd", 4) == 0) {
                    size_t field = i + 4 /*type*/ + 4 /*version+flags*/ +
                                   4 /*track id*/;
                    uint32_t drifted = spec.video_duration + 7;
                    bytes[field] = uint8_t(drifted >> 24);
                    bytes[field + 1] = uint8_t(drifted >> 16);
                    bytes[field + 2] = uint8_t(drifted >> 8);
                    bytes[field + 3] = uint8_t(drifted);
                    break;
                }
            }
            return out;
        }

        case Fault::Encrypted: {
            // A senc (sample encryption) box makes the protected nature
            // explicit; the parser must refuse it.
            auto out = normal;
            std::vector<uint8_t> senc_payload(16, 0);
            std::vector<uint8_t> senc;
            put_box(senc, "senc", senc_payload);
            out[0].second.insert(out[0].second.end(), senc.begin(),
                                 senc.end());
            return out;
        }

        case Fault::Truncated: {
            // Cut seg-05 in the middle of its moof header.
            auto out = normal;
            out[4].second.resize(out[4].second.size() / 2);
            return out;
        }
    }
    return normal;
}

}  // namespace mp4::fix
