#include "timeline.hpp"

#include <algorithm>
#include <cstdlib>
#include <limits>
#include <numeric>

namespace tl {

namespace {

std::int64_t checked_mul(std::int64_t a, std::int64_t b, bool* overflow) {
    if (a != 0 && std::llabs(b) >
                   std::numeric_limits<std::int64_t>::max() / std::llabs(a)) {
        *overflow = true;
        return 0;
    }
    return a * b;
}

bool parse_int64(const std::string& text, std::int64_t* value) {
    if (text.empty()) return false;
    std::size_t consumed = 0;
    try {
        *value = std::stoll(text, &consumed, 10);
    } catch (...) {
        return false;
    }
    return consumed == text.size();
}

}  // namespace

bool parse_rate_spec(const std::string& token, SpeedNode* node,
                     std::string* error) {
    const auto fail = [&](const char* msg) {
        if (error) *error = "bad speed token '" + token + "': " + msg;
        return false;
    };
    const std::size_t eq = token.find('=');
    const std::size_t slash = token.find('/', eq);
    if (eq == std::string::npos || slash == std::string::npos ||
        slash <= eq + 1 || slash + 1 >= token.size())
        return fail("expected frame=num/den");

    std::int64_t frame = 0;
    std::int64_t num = 0;
    std::int64_t den = 0;
    if (!parse_int64(token.substr(0, eq), &frame) ||
        !parse_int64(token.substr(eq + 1, slash - eq - 1), &num) ||
        !parse_int64(token.substr(slash + 1), &den))
        return fail("numbers must be integers");
    if (frame < 0) return fail("frame must be non-negative");
    if (num <= 0 || den <= 0) return fail("rate must be positive");

    const std::int64_t g = std::gcd(num, den);
    num /= g;
    den /= g;
    // Allowed range: 1/8 through 8/1, cross-multiplied for exactness.
    if (num * 8 < den) return fail("rate below 1/8");
    if (num > den * 8) return fail("rate above 8");

    node->output_frame = static_cast<int>(frame);
    node->rate = Rate{num, den};
    return true;
}

bool SpeedMap::build(std::vector<SpeedNode> nodes, int max_output,
                     std::string* error) {
    const auto fail = [&](const std::string& msg) {
        if (error) *error = msg;
        return false;
    };
    if (max_output < 0) return fail("max output frame is negative");
    if (nodes.empty()) return fail("speed map needs at least one node");
    // Input order is validated before any reordering: duplicate nodes
    // and nodes that move backwards in time are explicit errors rather
    // than being silently sorted away.
    for (std::size_t i = 1; i < nodes.size(); ++i) {
        if (nodes[i].output_frame == nodes[i - 1].output_frame)
            return fail("duplicate speed node frame");
        if (nodes[i].output_frame < nodes[i - 1].output_frame)
            return fail("speed nodes move backwards in time");
    }
    if (nodes.front().output_frame != 0)
        return fail("speed map must define a node at frame 0");
    if (nodes.back().output_frame > max_output)
        return fail("speed node frame beyond output range");

    const int slots = max_output + 1;
    pos_num_.assign(static_cast<std::size_t>(slots), 0);
    pos_den_.assign(static_cast<std::size_t>(slots), 1);

    std::size_t node_idx = 0;
    std::int64_t num = 0;
    std::int64_t den = 1;
    bool overflow = false;
    for (int f = 0; f < slots; ++f) {
        while (node_idx + 1 < nodes.size() &&
               nodes[node_idx + 1].output_frame <= f)
            ++node_idx;
        if (f > 0) {
            const Rate& r = nodes[node_idx].rate;
            num = checked_mul(num, r.den, &overflow) +
                  checked_mul(den, r.num, &overflow);
            den = checked_mul(den, r.den, &overflow);
            if (overflow) return fail("speed integration overflow");
            const std::int64_t g = std::gcd(num, den);
            num /= g;
            den /= g;
        }
        pos_num_[f] = num;
        pos_den_[f] = den;
    }
    nodes_ = std::move(nodes);
    max_output_ = max_output;
    return true;
}

std::int64_t SpeedMap::position_num(int frame) const {
    return pos_num_[static_cast<std::size_t>(frame)];
}
std::int64_t SpeedMap::position_den(int frame) const {
    return pos_den_[static_cast<std::size_t>(frame)];
}

int SpeedMap::source_frame(int output_frame) const {
    const std::int64_t num = position_num(output_frame);
    const std::int64_t den = position_den(output_frame);
    // Nearest integer with symmetric rounding (half rounds up).
    return static_cast<int>((num + den / 2) / den);
}

FrameBuffer Timeline::source_frame_buffer(int clip, int index) const {
    if (clip == 0)
        return make_checkerboard(width_, height_, index, 8, seed_);
    return make_gradient(width_, height_, index, seed_);
}

bool Timeline::build(int width, int height, int output_frames,
                     int transition_start, int transition_length,
                     TransitionType type, const TransitionParams& params,
                     const SpeedMap& speed, std::uint64_t seed,
                     std::string* error) {
    const auto fail = [&](const std::string& msg) {
        if (error) *error = msg;
        return false;
    };
    if (!valid_dimensions(width, height))
        return fail("invalid frame dimensions");
    if (output_frames <= 0 || output_frames > kMaxFrames)
        return fail("invalid output frame count");
    if (transition_start < 0 || transition_length <= 0 ||
        transition_start + transition_length > output_frames)
        return fail("transition window outside timeline");
    if (speed.nodes().empty()) return fail("speed map is empty");

    width_ = width;
    height_ = height;
    output_frames_ = output_frames;
    transition_start_ = transition_start;
    transition_length_ = transition_length;
    type_ = type;
    params_ = params;
    speed_ = speed;
    seed_ = seed;
    // One source slot beyond the furthest mapped index as headroom.
    source_frames_ = speed_.source_frame(output_frames - 1) + 2;
    if (source_frames_ <= 0 || source_frames_ > kMaxFrames)
        return fail("source frame count out of range");
    return true;
}

bool Timeline::render_frame(int output_frame, FrameBuffer* out,
                            std::string* error) const {
    if (out == nullptr) {
        if (error) *error = "null output frame";
        return false;
    }
    if (output_frame < 0 || output_frame >= output_frames_) {
        if (error) *error = "output frame out of range";
        return false;
    }
    const int src_index = speed_.source_frame(output_frame);
    const int clamped = std::clamp(src_index, 0, source_frames_ - 1);

    *out = FrameBuffer(width_, height_);
    const int transition_end = transition_start_ + transition_length_;
    if (output_frame < transition_start_) {
        *out = source_frame_buffer(0, clamped);
        return true;
    }
    if (output_frame >= transition_end) {
        *out = source_frame_buffer(1, clamped);
        return true;
    }

    FrameBuffer from = source_frame_buffer(0, clamped);
    FrameBuffer to = source_frame_buffer(1, clamped);
    const int step = output_frame - transition_start_;
    return render_transition(*out, from, to, step, transition_length_ - 1,
                             type_, params_, error);
}

}  // namespace tl
