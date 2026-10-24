#pragma once
#include <stddef.h>
#ifdef __cplusplus
extern "C" {
#endif
/* zlib crc32 of data. */
unsigned long pack_crc32(const void *data, size_t len);
/* Lowercase hex SHA-256 (libcrypto) of data into out (65 bytes). Returns 0 on success. */
int pack_sha256_hex(const void *data, size_t len, char out[65]);
#ifdef __cplusplus
}
#endif
