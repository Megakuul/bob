#include <stdio.h>

#include "rcore.h"
#include "rdyn.h"

int main(void) {
    char buf[32];
    rcore_num_hex(7, buf, sizeof buf);
    printf("rdyn_add(40,2)=%lld rcore_num_hex(7)=%s\n", (long long)rdyn_add(40, 2), buf);
    return 0;
}
