#include "pack.h"

#include <cstdio>
#include <openssl/evp.h>
#include <zlib.h>

extern "C" unsigned long pack_crc32(const void *data, size_t len) {
    return crc32(0L, static_cast<const Bytef *>(data), static_cast<uInt>(len));
}

extern "C" int pack_sha256_hex(const void *data, size_t len, char out[65]) {
    unsigned char md[EVP_MAX_MD_SIZE];
    unsigned int n = 0;
    if (EVP_Digest(data, len, md, &n, EVP_sha256(), nullptr) != 1) return 1;
    for (unsigned int i = 0; i < n; i++) std::snprintf(out + 2 * i, 3, "%02x", md[i]);
    out[64] = '\0';
    return 0;
}
