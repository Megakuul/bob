#pragma once
#ifdef __cplusplus
extern "C" {
#endif
/* Writes the lowercase hex SHA-256 of msg into out (65 bytes incl. NUL). Returns 0 on success. */
int foo_sha256_hex(const char *msg, char out[65]);
#ifdef __cplusplus
}
#endif
