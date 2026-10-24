#include "foo.h"

unsigned int foo_checksum(const char *s) {
    unsigned int h = 7;
    for (; *s; s++) h = h * 31 + (unsigned char)*s;
    return h;
}
