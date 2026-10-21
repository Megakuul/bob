/* app: uses a static lib, a shared lib (zlib + libcrypto) and an External (zstd). */
#include <stdio.h>
#include <string.h>
#include <zstd.h>

#include "mathx.h"
#include "pack.h"

int main(void) {
    const char *msg = "bob builds c";
    char hex[65];
    char buf[256];
    size_t z = ZSTD_compress(buf, sizeof buf, msg, strlen(msg), 3);
    if (ZSTD_isError(z) || pack_sha256_hex(msg, strlen(msg), hex) != 0) return 1;
    printf("gcd=%ld isqrt=%lu crc32=%08lx sha256=%.16s zstd=%zu->%zu\n", mathx_gcd(84, 36),
           mathx_isqrt(1000), pack_crc32(msg, strlen(msg)), hex, strlen(msg), z);
    return 0;
}
