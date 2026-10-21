// TAP test for libfoo: sha256("bob") is a fixed digest.
#include <cstdio>
#include <cstring>
#include "foo.h"

int main() {
    char out[65];
    std::printf("TAP version 14\n1..1\n");
    bool ok = foo_sha256_hex("bob", out) == 0 &&
              std::strcmp(out, "81b637d8fcd2c6da6359e6963113a1170de795e4b725b84d1e0b4cfd9ec58ce9") == 0;
    std::printf("%s 1 - sha256(bob)\n", ok ? "ok" : "not ok");
    return ok ? 0 : 1;
}
