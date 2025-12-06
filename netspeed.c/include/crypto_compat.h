/*
 * crypto_compat.h - Crypto compatibility layer for OpenSSL and LibreSSL
 *
 * Provides a consistent interface that works with:
 * - OpenSSL 1.1+
 * - OpenSSL 3.0+ (avoids deprecated functions)
 * - LibreSSL 2.7+
 * - LibreSSL < 2.7 (fallback to older API)
 */

#ifndef NETSPEED_CRYPTO_COMPAT_H
#define NETSPEED_CRYPTO_COMPAT_H

#include <stdint.h>
#include <stddef.h>
#include <string.h>

/*
 * Detect crypto library
 */
#if defined(LIBRESSL_VERSION_NUMBER)
    #define NETSPEED_USING_LIBRESSL 1
    /* LibreSSL 2.7.0 = 0x2070000f */
    #if LIBRESSL_VERSION_NUMBER >= 0x2070000fL
        #define NETSPEED_HAS_EVP_MD_CTX_NEW 1
    #else
        #define NETSPEED_HAS_EVP_MD_CTX_NEW 0
    #endif
#elif defined(OPENSSL_VERSION_NUMBER)
    #define NETSPEED_USING_OPENSSL 1
    /* OpenSSL 1.1.0 added EVP_MD_CTX_new */
    #if OPENSSL_VERSION_NUMBER >= 0x10100000L
        #define NETSPEED_HAS_EVP_MD_CTX_NEW 1
    #else
        #define NETSPEED_HAS_EVP_MD_CTX_NEW 0
    #endif
#else
    /* Unknown library - assume modern API */
    #define NETSPEED_HAS_EVP_MD_CTX_NEW 1
#endif

#include <openssl/evp.h>
#include <openssl/hmac.h>
#include <openssl/rand.h>

/*
 * MD5 hash computation
 *
 * Computes MD5(data) and stores result in out (must be 16 bytes).
 */
static inline void crypto_md5(const void *data, size_t len, uint8_t out[16])
{
#if NETSPEED_HAS_EVP_MD_CTX_NEW
    EVP_MD_CTX *ctx = EVP_MD_CTX_new();
    if (ctx) {
        EVP_DigestInit_ex(ctx, EVP_md5(), NULL);
        EVP_DigestUpdate(ctx, data, len);
        EVP_DigestFinal_ex(ctx, out, NULL);
        EVP_MD_CTX_free(ctx);
    }
#else
    /* Fallback for older LibreSSL/OpenSSL - use stack allocation */
    EVP_MD_CTX ctx;
    EVP_MD_CTX_init(&ctx);
    EVP_DigestInit_ex(&ctx, EVP_md5(), NULL);
    EVP_DigestUpdate(&ctx, data, len);
    EVP_DigestFinal_ex(&ctx, out, NULL);
    EVP_MD_CTX_cleanup(&ctx);
#endif
}

/*
 * HMAC-SHA1 computation
 *
 * Computes HMAC-SHA1(key, data) and stores result in out (must be 20 bytes).
 * Returns the HMAC length (20).
 */
static inline unsigned int crypto_hmac_sha1(const void *key, size_t key_len,
                                            const void *data, size_t data_len,
                                            uint8_t out[20])
{
    unsigned int hmac_len = 0;
    HMAC(EVP_sha1(), key, key_len, data, data_len, out, &hmac_len);
    return hmac_len;
}

/*
 * Generate random bytes
 *
 * Fills buffer with cryptographically secure random bytes.
 * Returns 1 on success, 0 on failure.
 */
static inline int crypto_random_bytes(void *buf, size_t len)
{
    return RAND_bytes(buf, len);
}

#endif /* NETSPEED_CRYPTO_COMPAT_H */
