#include <cstdio>

#include "ring/ring.hpp"

int main() {
    ring::Buffer b(3);
    for (int i = 1; i <= 5; i++) b.push(i);
    bool ok = b.size() == 3 && b.sum() == 12;
    std::printf("TAP version 14\n1..1\n%s 1 - ring keeps last 3\n", ok ? "ok" : "not ok");
    return ok ? 0 : 1;
}
