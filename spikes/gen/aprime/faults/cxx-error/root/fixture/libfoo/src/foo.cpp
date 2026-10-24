#include "foo.h"
#include <openssl/evp.h>
#include <string>
#include <cstdio>

extern "C" int foo_sha256_hex(const char *msg, char out[65]) {
    unsigned char md[EVP_MAX_MD_SIZE];
    unsigned int len = 0;
    std::string s(msg);
    if (EVP_Digest(s.data(), s.size(), md, &len, EVP_sha256(), nullptr) != 1) return 1;
    for (unsigned int i = 0; i < len; i++) std::snprintf(out + 2 * i, 3, "%02x", md[i]);
    out[64] = '\0';
    return 0 
}
