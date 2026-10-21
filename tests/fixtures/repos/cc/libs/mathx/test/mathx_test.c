/* TAP test for mathx. */
#include <stdio.h>
#include "mathx.h"

static int n;
static int fails;
static void check(int ok, const char *what) {
    n++;
    if (!ok) fails++;
    printf("%s %d - %s\n", ok ? "ok" : "not ok", n, what);
}

int main(void) {
    printf("TAP version 14\n1..3\n");
    check(mathx_gcd(12, 18) == 6, "gcd(12,18)");
    check(mathx_gcd(-7, 21) == 7, "gcd(-7,21)");
    check(mathx_isqrt(99) == 9, "isqrt(99)");
    return fails != 0;
}
