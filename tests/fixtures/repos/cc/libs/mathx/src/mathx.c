#include "mathx.h"

long mathx_gcd(long a, long b) {
    if (a < 0) a = -a;
    if (b < 0) b = -b;
    while (b != 0) {
        long t = a % b;
        a = b;
        b = t;
    }
    return a;
}

unsigned long mathx_isqrt(unsigned long n) {
    unsigned long r = 0;
    while ((r + 1) * (r + 1) <= n) r++;
    return r;
}
