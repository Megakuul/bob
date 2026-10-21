#include <cstdio>

#include "ring/ring.hpp"

int main() {
    ring::Buffer b(4);
    for (int i = 1; i <= 10; i++) b.push(i * i);
    std::printf("ring size=%zu sum=%d\n", b.size(), b.sum());
    return 0;
}
