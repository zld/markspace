// Transition Lab — offline cutscene timeline verification tool.
// This file is the command entry point; modules are wired in across
// incremental commits, each of which remains independently buildable.

#include <cstdio>

int main(int argc, char** argv) {
    if (argc < 2) {
        std::fprintf(stderr,
                     "transition_lab: missing command (expected 'demo')\n");
        return 2;
    }
    std::printf("transition_lab skeleton: command '%s' not yet wired\n",
                argv[1]);
    return 0;
}
