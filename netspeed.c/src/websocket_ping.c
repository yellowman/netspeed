/*
 * websocket_ping.c - Persistent application-level WebSocket latency echo.
 *
 * The implementation deliberately uses libcurl's portable connected-socket
 * API rather than its optional experimental WebSocket build feature. That
 * keeps the C client capable on ordinary libcurl builds while still letting
 * libcurl own DNS, TCP, proxy tunneling, and TLS. Browsers cannot originate
 * WebSocket control ping frames, so every Netspeed client uses the same fixed
 * 16-byte binary NSP1 nonce and requires an exact echo.
 */
#include "websocket_ping.h"

#include "timing.h"

#include <ctype.h>
#include <errno.h>
#include <inttypes.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#ifdef _WIN32
#include <winsock2.h>
#else
#include <sys/select.h>
#include <sys/time.h>
#include <unistd.h>
#endif

#define USER_AGENT "netspeed-c/" NETSPEED_VERSION
#define WEBSOCKET_GUID "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
#define WEBSOCKET_IO_SLICE_MS 250
#define WEBSOCKET_HANDSHAKE_LIMIT 16384U
#define WEBSOCKET_FRAME_LIMIT 125U

typedef struct {
    int status;
    char upgrade[64];
    char connection[128];
    char accept[128];
    int accept_count;
    bool accept_conflict;
    char protocol[64];
    int protocol_count;
    bool protocol_conflict;
    char cache_control[512];
    char content_encoding[256];
    char x_accel_buffering[128];
    char measurement[64];
    bool overflow;
} handshake_headers_t;

typedef struct {
    uint32_t state[5];
    uint64_t total_bytes;
    unsigned char block[64];
    size_t block_length;
} sha1_context_t;

static atomic_uint_fast64_t nonce_counter = ATOMIC_VAR_INIT(UINT64_C(1));

static void set_error(websocket_ping_session_t *session, const char *message)
{
    if (!session) {
        return;
    }
    snprintf(session->error, sizeof(session->error), "%s",
             message && *message ? message : "WebSocket transport error");
}

static char *trim(char *value)
{
    while (*value && isspace((unsigned char)*value)) {
        value++;
    }
    char *end = value + strlen(value);
    while (end > value && isspace((unsigned char)end[-1])) {
        *--end = '\0';
    }
    return value;
}

static bool append_value(char *destination, size_t capacity, const char *value)
{
    size_t used = strnlen(destination, capacity);
    size_t value_length = strlen(value);
    size_t separator = used ? 2U : 0U;
    if (used >= capacity || separator > capacity - used - 1U ||
        value_length > capacity - used - separator - 1U) {
        return false;
    }
    if (separator) {
        destination[used++] = ',';
        destination[used++] = ' ';
    }
    memcpy(destination + used, value, value_length + 1U);
    return true;
}

static bool header_has_token(const char *value, const char *target)
{
    if (!value || !*value) {
        return false;
    }
    char copy[512];
    if (strlen(value) >= sizeof(copy)) {
        return false;
    }
    snprintf(copy, sizeof(copy), "%s", value);
    char *save = NULL;
    for (char *token = strtok_r(copy, ",", &save); token;
         token = strtok_r(NULL, ",", &save)) {
        if (strcasecmp(trim(token), target) == 0) {
            return true;
        }
    }
    return false;
}

static bool cache_has_directive(const char *value, const char *target)
{
    if (!value || !*value) {
        return false;
    }
    char copy[512];
    if (strlen(value) >= sizeof(copy)) {
        return false;
    }
    snprintf(copy, sizeof(copy), "%s", value);
    char *save = NULL;
    for (char *token = strtok_r(copy, ",", &save); token;
         token = strtok_r(NULL, ",", &save)) {
        char *item = trim(token);
        char *parameter = strchr(item, ';');
        if (parameter) {
            *parameter = '\0';
        }
        if (strcasecmp(trim(item), target) == 0) {
            return true;
        }
    }
    return false;
}

static bool identity_encoding_only(const char *value)
{
    if (!value || !*value) {
        return true;
    }
    char copy[256];
    if (strlen(value) >= sizeof(copy)) {
        return false;
    }
    snprintf(copy, sizeof(copy), "%s", value);
    char *save = NULL;
    bool saw = false;
    for (char *token = strtok_r(copy, ",", &save); token;
         token = strtok_r(NULL, ",", &save)) {
        char *coding = trim(token);
        if (!*coding || strcasecmp(coding, "identity") != 0) {
            return false;
        }
        saw = true;
    }
    return saw;
}

static bool all_header_values_equal(const char *value, const char *target)
{
    if (!value || !*value) {
        return false;
    }
    char copy[256];
    if (strlen(value) >= sizeof(copy)) {
        return false;
    }
    snprintf(copy, sizeof(copy), "%s", value);
    char *save = NULL;
    bool saw = false;
    for (char *token = strtok_r(copy, ",", &save); token;
         token = strtok_r(NULL, ",", &save)) {
        char *item = trim(token);
        if (!*item || strcasecmp(item, target) != 0) {
            return false;
        }
        saw = true;
    }
    return saw;
}

static uint32_t rotate_left(uint32_t value, unsigned int bits)
{
    return (value << bits) | (value >> (32U - bits));
}

static void sha1_transform(sha1_context_t *context,
                           const unsigned char block[64])
{
    uint32_t words[80];
    for (int index = 0; index < 16; index++) {
        words[index] = ((uint32_t)block[index * 4] << 24) |
                       ((uint32_t)block[index * 4 + 1] << 16) |
                       ((uint32_t)block[index * 4 + 2] << 8) |
                       (uint32_t)block[index * 4 + 3];
    }
    for (int index = 16; index < 80; index++) {
        words[index] = rotate_left(words[index - 3] ^ words[index - 8] ^
                                   words[index - 14] ^ words[index - 16], 1);
    }

    uint32_t a = context->state[0];
    uint32_t b = context->state[1];
    uint32_t c = context->state[2];
    uint32_t d = context->state[3];
    uint32_t e = context->state[4];
    for (int index = 0; index < 80; index++) {
        uint32_t function;
        uint32_t constant;
        if (index < 20) {
            function = (b & c) | ((~b) & d);
            constant = UINT32_C(0x5a827999);
        } else if (index < 40) {
            function = b ^ c ^ d;
            constant = UINT32_C(0x6ed9eba1);
        } else if (index < 60) {
            function = (b & c) | (b & d) | (c & d);
            constant = UINT32_C(0x8f1bbcdc);
        } else {
            function = b ^ c ^ d;
            constant = UINT32_C(0xca62c1d6);
        }
        uint32_t temporary = rotate_left(a, 5) + function + e + constant +
                             words[index];
        e = d;
        d = c;
        c = rotate_left(b, 30);
        b = a;
        a = temporary;
    }
    context->state[0] += a;
    context->state[1] += b;
    context->state[2] += c;
    context->state[3] += d;
    context->state[4] += e;
}

static void sha1_init(sha1_context_t *context)
{
    memset(context, 0, sizeof(*context));
    context->state[0] = UINT32_C(0x67452301);
    context->state[1] = UINT32_C(0xefcdab89);
    context->state[2] = UINT32_C(0x98badcfe);
    context->state[3] = UINT32_C(0x10325476);
    context->state[4] = UINT32_C(0xc3d2e1f0);
}

static void sha1_update(sha1_context_t *context,
                        const unsigned char *data, size_t length)
{
    context->total_bytes += length;
    while (length) {
        size_t available = sizeof(context->block) - context->block_length;
        size_t amount = length < available ? length : available;
        memcpy(context->block + context->block_length, data, amount);
        context->block_length += amount;
        data += amount;
        length -= amount;
        if (context->block_length == sizeof(context->block)) {
            sha1_transform(context, context->block);
            context->block_length = 0;
        }
    }
}

static void sha1_final(sha1_context_t *context, unsigned char digest[20])
{
    uint64_t bit_length = context->total_bytes * UINT64_C(8);
    unsigned char padding[72];
    memset(padding, 0, sizeof(padding));
    padding[0] = 0x80;
    size_t padding_length = context->block_length < 56
                                ? 56 - context->block_length
                                : 120 - context->block_length;
    sha1_update(context, padding, padding_length);
    unsigned char encoded_length[8];
    for (int index = 0; index < 8; index++) {
        encoded_length[index] =
            (unsigned char)(bit_length >> (56 - index * 8));
    }
    sha1_update(context, encoded_length, sizeof(encoded_length));
    for (int index = 0; index < 5; index++) {
        digest[index * 4] = (unsigned char)(context->state[index] >> 24);
        digest[index * 4 + 1] = (unsigned char)(context->state[index] >> 16);
        digest[index * 4 + 2] = (unsigned char)(context->state[index] >> 8);
        digest[index * 4 + 3] = (unsigned char)context->state[index];
    }
}

static bool base64_encode(const unsigned char *input, size_t length,
                          char *output, size_t capacity)
{
    static const char alphabet[] =
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    size_t required = ((length + 2U) / 3U) * 4U;
    if (capacity <= required) {
        return false;
    }
    size_t source = 0;
    size_t destination = 0;
    while (source < length) {
        size_t remaining = length - source;
        uint32_t value = (uint32_t)input[source] << 16;
        if (remaining > 1) value |= (uint32_t)input[source + 1] << 8;
        if (remaining > 2) value |= input[source + 2];
        output[destination++] = alphabet[(value >> 18) & 0x3fU];
        output[destination++] = alphabet[(value >> 12) & 0x3fU];
        output[destination++] = remaining > 1 ? alphabet[(value >> 6) & 0x3fU] : '=';
        output[destination++] = remaining > 2 ? alphabet[value & 0x3fU] : '=';
        source += remaining >= 3 ? 3 : remaining;
    }
    output[destination] = '\0';
    return true;
}

static uint64_t next_random_word(const void *address)
{
    struct timespec now;
    timing_now(&now);
    uint64_t value = atomic_fetch_add(&nonce_counter,
                                      UINT64_C(0x9e3779b97f4a7c15));
    value ^= (uint64_t)now.tv_sec;
    value ^= (uint64_t)now.tv_nsec << 32;
    value ^= (uint64_t)(uintptr_t)address;
    value ^= value >> 12;
    value ^= value << 25;
    value ^= value >> 27;
    return value * UINT64_C(0x2545f4914f6cdd1d);
}

static void random_bytes(unsigned char *destination, size_t length)
{
    uint64_t word = 0;
    for (size_t index = 0; index < length; index++) {
        if ((index & 7U) == 0) {
            word = next_random_word(destination + index);
        }
        destination[index] = (unsigned char)(word >> ((index & 7U) * 8U));
    }
}

static int build_http_target(const char *base, const char *path,
                             char *url, size_t url_capacity,
                             char *authority, size_t authority_capacity,
                             char *request_target, size_t target_capacity)
{
    if (!base || !*base || !path || path[0] != '/') {
        return ERR_ARGS;
    }
    int written = snprintf(url, url_capacity, "%s%s", base, path);
    if (written < 0 || (size_t)written >= url_capacity) {
        return ERR_ARGS;
    }
    const char *scheme_end = strstr(url, "://");
    bool http_scheme = scheme_end && (size_t)(scheme_end - url) == 4 &&
                       strncasecmp(url, "http", 4) == 0;
    bool https_scheme = scheme_end && (size_t)(scheme_end - url) == 5 &&
                        strncasecmp(url, "https", 5) == 0;
    if (!http_scheme && !https_scheme) {
        return ERR_ARGS;
    }
    const char *authority_start = scheme_end + 3;
    const char *target_start = strchr(authority_start, '/');
    const char *authority_end = target_start ? target_start : url + strlen(url);
    size_t authority_length = (size_t)(authority_end - authority_start);
    if (authority_length == 0 || authority_length >= authority_capacity ||
        memchr(authority_start, '@', authority_length) ||
        memchr(authority_start, '\r', authority_length) ||
        memchr(authority_start, '\n', authority_length)) {
        return ERR_ARGS;
    }
    memcpy(authority, authority_start, authority_length);
    authority[authority_length] = '\0';
    const char *target = target_start ? target_start : "/";
    if (strchr(target, '\r') || strchr(target, '\n') || strchr(target, '#') ||
        strlen(target) >= target_capacity) {
        return ERR_ARGS;
    }
    snprintf(request_target, target_capacity, "%s", target);
    return ERR_OK;
}

static int transfer_progress(void *opaque, curl_off_t down_total,
                             curl_off_t down_now, curl_off_t up_total,
                             curl_off_t up_now)
{
    (void)down_total;
    (void)down_now;
    (void)up_total;
    (void)up_now;
    const http_client_t *client = opaque;
    return client && client->aborted && *client->aborted ? 1 : 0;
}

static int wait_socket(websocket_ping_session_t *session, bool writable,
                       int64_t deadline_ms)
{
    curl_socket_t socket_handle = CURL_SOCKET_BAD;
    if (curl_easy_getinfo(session->easy, CURLINFO_ACTIVESOCKET,
                          &socket_handle) != CURLE_OK ||
        socket_handle == CURL_SOCKET_BAD) {
        set_error(session, "WebSocket socket is unavailable");
        return ERR_NETWORK;
    }
    while (timing_monotonic_ms() < deadline_ms) {
        if (session->client->aborted && *session->client->aborted) {
            set_error(session, "WebSocket ping interrupted");
            return ERR_TIMEOUT;
        }
        int64_t remaining = deadline_ms - timing_monotonic_ms();
        int timeout_ms = remaining < WEBSOCKET_IO_SLICE_MS
                             ? (int)remaining
                             : WEBSOCKET_IO_SLICE_MS;
        if (timeout_ms < 1) timeout_ms = 1;
        fd_set read_set;
        fd_set write_set;
        FD_ZERO(&read_set);
        FD_ZERO(&write_set);
        if (writable) FD_SET(socket_handle, &write_set);
        else FD_SET(socket_handle, &read_set);
        struct timeval timeout = {
            .tv_sec = timeout_ms / 1000,
            .tv_usec = (timeout_ms % 1000) * 1000,
        };
#ifdef _WIN32
        int ready = select(0, writable ? NULL : &read_set,
                           writable ? &write_set : NULL, NULL, &timeout);
#else
        int ready = select((int)socket_handle + 1,
                           writable ? NULL : &read_set,
                           writable ? &write_set : NULL, NULL, &timeout);
#endif
        if (ready > 0) {
            return ERR_OK;
        }
        if (ready < 0) {
#ifdef _WIN32
            set_error(session, "WebSocket socket wait failed");
#else
            if (errno == EINTR) {
                continue;
            }
            snprintf(session->error, sizeof(session->error),
                     "WebSocket socket wait failed: %s", strerror(errno));
#endif
            return ERR_NETWORK;
        }
    }
    set_error(session, "WebSocket ping timed out");
    return ERR_TIMEOUT;
}

static int send_all(websocket_ping_session_t *session,
                    const unsigned char *data, size_t length,
                    int64_t deadline_ms)
{
    size_t offset = 0;
    while (offset < length) {
        size_t sent = 0;
        CURLcode code = curl_easy_send(session->easy, data + offset,
                                       length - offset, &sent);
        if (code == CURLE_OK) {
            if (sent == 0) {
                set_error(session, "WebSocket connection closed while sending");
                return ERR_NETWORK;
            }
            offset += sent;
            continue;
        }
        if (code != CURLE_AGAIN) {
            snprintf(session->error, sizeof(session->error),
                     "WebSocket send failed: %s", curl_easy_strerror(code));
            return ERR_NETWORK;
        }
        int status = wait_socket(session, true, deadline_ms);
        if (status != ERR_OK) {
            return status;
        }
    }
    return ERR_OK;
}

static int receive_some(websocket_ping_session_t *session,
                        unsigned char *data, size_t capacity,
                        size_t *received, int64_t deadline_ms)
{
    for (;;) {
        CURLcode code = curl_easy_recv(session->easy, data, capacity, received);
        if (code == CURLE_OK) {
            if (*received == 0) {
                set_error(session, "WebSocket peer closed the connection");
                return ERR_NETWORK;
            }
            return ERR_OK;
        }
        if (code != CURLE_AGAIN) {
            snprintf(session->error, sizeof(session->error),
                     "WebSocket receive failed: %s", curl_easy_strerror(code));
            return ERR_NETWORK;
        }
        int status = wait_socket(session, false, deadline_ms);
        if (status != ERR_OK) {
            return status;
        }
    }
}

static int receive_exact(websocket_ping_session_t *session,
                         unsigned char *data, size_t length,
                         int64_t deadline_ms)
{
    size_t offset = 0;
    while (offset < length) {
        size_t received = 0;
        int status = receive_some(session, data + offset, length - offset,
                                  &received, deadline_ms);
        if (status != ERR_OK) {
            return status;
        }
        offset += received;
    }
    return ERR_OK;
}

static int send_frame(websocket_ping_session_t *session, unsigned char opcode,
                      const unsigned char *payload, size_t length,
                      int64_t deadline_ms)
{
    if (length > WEBSOCKET_FRAME_LIMIT) {
        set_error(session, "WebSocket latency frame exceeded 125 bytes");
        return ERR_PROTOCOL;
    }
    unsigned char frame[2 + 4 + WEBSOCKET_FRAME_LIMIT];
    unsigned char mask[4];
    random_bytes(mask, sizeof(mask));
    frame[0] = (unsigned char)(0x80U | (opcode & 0x0fU));
    frame[1] = (unsigned char)(0x80U | length);
    memcpy(frame + 2, mask, sizeof(mask));
    for (size_t index = 0; index < length; index++) {
        frame[6 + index] = payload[index] ^ mask[index & 3U];
    }
    return send_all(session, frame, 6U + length, deadline_ms);
}

static int receive_frame(websocket_ping_session_t *session,
                         unsigned char *opcode,
                         unsigned char *payload, size_t capacity,
                         size_t *length, int64_t deadline_ms)
{
    unsigned char header[2];
    int status = receive_exact(session, header, sizeof(header), deadline_ms);
    if (status != ERR_OK) {
        return status;
    }
    bool final = (header[0] & 0x80U) != 0;
    bool reserved = (header[0] & 0x70U) != 0;
    bool masked = (header[1] & 0x80U) != 0;
    *opcode = header[0] & 0x0fU;
    uint64_t payload_length = header[1] & 0x7fU;
    if (reserved || masked || !final) {
        set_error(session,
                  "WebSocket latency response used reserved, masked, or fragmented framing");
        return ERR_PROTOCOL;
    }
    if (payload_length == 126U) {
        unsigned char extended[2];
        status = receive_exact(session, extended, sizeof(extended), deadline_ms);
        if (status != ERR_OK) return status;
        payload_length = ((uint64_t)extended[0] << 8) | extended[1];
    } else if (payload_length == 127U) {
        unsigned char extended[8];
        status = receive_exact(session, extended, sizeof(extended), deadline_ms);
        if (status != ERR_OK) return status;
        if (extended[0] & 0x80U) {
            set_error(session, "WebSocket latency response used an invalid frame length");
            return ERR_PROTOCOL;
        }
        payload_length = 0;
        for (int index = 0; index < 8; index++) {
            payload_length = (payload_length << 8) | extended[index];
        }
    }
    bool control = (*opcode & 0x08U) != 0;
    if ((control && payload_length > WEBSOCKET_FRAME_LIMIT) ||
        payload_length > capacity) {
        set_error(session, "WebSocket latency response exceeded the permitted payload size");
        return ERR_PROTOCOL;
    }
    if (payload_length) {
        status = receive_exact(session, payload, (size_t)payload_length,
                               deadline_ms);
        if (status != ERR_OK) return status;
    }
    *length = (size_t)payload_length;
    return ERR_OK;
}

static bool parse_singleton(char *destination, size_t capacity,
                            int *count, bool *conflict,
                            const char *value)
{
    (*count)++;
    if (strchr(value, ',') ||
        (destination[0] && strcasecmp(destination, value) != 0)) {
        *conflict = true;
    }
    if (!destination[0]) {
        if (strlen(value) >= capacity) {
            return false;
        }
        snprintf(destination, capacity, "%s", value);
    }
    return true;
}

static bool parse_handshake_response(char *response,
                                     handshake_headers_t *headers)
{
    memset(headers, 0, sizeof(*headers));
    char *line_end = strstr(response, "\r\n");
    if (!line_end) {
        return false;
    }
    *line_end = '\0';
    if (sscanf(response, "HTTP/%*u.%*u %d", &headers->status) != 1) {
        return false;
    }
    char *cursor = line_end + 2;
    while (*cursor) {
        line_end = strstr(cursor, "\r\n");
        if (!line_end) {
            return false;
        }
        if (line_end == cursor) {
            return true;
        }
        *line_end = '\0';
        char *colon = strchr(cursor, ':');
        if (!colon) {
            return false;
        }
        *colon = '\0';
        char *name = trim(cursor);
        char *value = trim(colon + 1);
        if (strcasecmp(name, "Upgrade") == 0) {
            if (!append_value(headers->upgrade, sizeof(headers->upgrade), value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "Connection") == 0) {
            if (!append_value(headers->connection,
                              sizeof(headers->connection), value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "Sec-WebSocket-Accept") == 0) {
            if (!parse_singleton(headers->accept, sizeof(headers->accept),
                                 &headers->accept_count,
                                 &headers->accept_conflict, value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "Sec-WebSocket-Protocol") == 0) {
            if (!parse_singleton(headers->protocol, sizeof(headers->protocol),
                                 &headers->protocol_count,
                                 &headers->protocol_conflict, value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "Cache-Control") == 0) {
            if (!append_value(headers->cache_control,
                              sizeof(headers->cache_control), value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "Content-Encoding") == 0) {
            if (!append_value(headers->content_encoding,
                              sizeof(headers->content_encoding), value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "X-Accel-Buffering") == 0) {
            if (!append_value(headers->x_accel_buffering,
                              sizeof(headers->x_accel_buffering), value)) {
                headers->overflow = true;
            }
        } else if (strcasecmp(name, "X-Netspeed-Measurement") == 0) {
            if (!append_value(headers->measurement,
                              sizeof(headers->measurement), value)) {
                headers->overflow = true;
            }
        }
        cursor = line_end + 2;
    }
    return false;
}

static void build_payload(uint32_t sequence,
                          unsigned char payload[NETSPEED_WEBSOCKET_PING_PAYLOAD_BYTES])
{
    payload[0] = 'N';
    payload[1] = 'S';
    payload[2] = 'P';
    payload[3] = '1';
    payload[4] = (unsigned char)(sequence >> 24);
    payload[5] = (unsigned char)(sequence >> 16);
    payload[6] = (unsigned char)(sequence >> 8);
    payload[7] = (unsigned char)sequence;
    random_bytes(payload + 8, 8);
}

static bool payload_equal(const unsigned char *left,
                          const unsigned char *right, size_t length)
{
    unsigned char difference = 0;
    for (size_t index = 0; index < length; index++) {
        difference |= left[index] ^ right[index];
    }
    return difference == 0;
}

bool websocket_ping_supported(void)
{
    return true;
}

void websocket_ping_session_init(websocket_ping_session_t *session,
                                 const http_client_t *client)
{
    if (!session) return;
    memset(session, 0, sizeof(*session));
    session->client = client;
}

void websocket_ping_session_cleanup(websocket_ping_session_t *session)
{
    if (!session) return;
    if (session->easy) {
        curl_easy_cleanup(session->easy);
    }
    const http_client_t *client = session->client;
    memset(session, 0, sizeof(*session));
    session->client = client;
}

const char *websocket_ping_error(const websocket_ping_session_t *session)
{
    if (!session || !session->error[0]) {
        return "WebSocket latency transport error";
    }
    return session->error;
}

int websocket_ping_open(websocket_ping_session_t *session,
                        const char *endpoint_path,
                        const char *protocol,
                        int payload_bytes)
{
    if (!session || !session->client || !endpoint_path || !*endpoint_path ||
        !protocol || strcmp(protocol, NETSPEED_WEBSOCKET_PING_PROTOCOL) != 0 ||
        payload_bytes != NETSPEED_WEBSOCKET_PING_PAYLOAD_BYTES) {
        if (session) set_error(session, "unsupported WebSocket latency contract");
        return ERR_PROTOCOL;
    }
    websocket_ping_session_cleanup(session);
    if (!session->client) {
        set_error(session, "WebSocket latency client is missing");
        return ERR_ARGS;
    }

    char url[MAX_URL_LEN * 2];
    char authority[MAX_URL_LEN];
    char request_target[MAX_URL_LEN * 2];
    if (build_http_target(session->client->base_url, endpoint_path,
                          url, sizeof(url), authority, sizeof(authority),
                          request_target, sizeof(request_target)) != ERR_OK) {
        set_error(session, "WebSocket latency URL is too long or invalid");
        return ERR_ARGS;
    }
    if (strchr(session->client->access_token, '\r') ||
        strchr(session->client->access_token, '\n')) {
        set_error(session, "WebSocket bearer token contains an invalid line break");
        return ERR_ARGS;
    }

    session->easy = curl_easy_init();
    if (!session->easy) {
        set_error(session, "failed to initialize WebSocket latency transport");
        return ERR_MEMORY;
    }
    snprintf(session->endpoint_path, sizeof(session->endpoint_path), "%s",
             endpoint_path);

    CURL *easy = session->easy;
    curl_easy_setopt(easy, CURLOPT_URL, url);
    curl_easy_setopt(easy, CURLOPT_CONNECT_ONLY, 1L);
    curl_easy_setopt(easy, CURLOPT_HTTP_VERSION, CURL_HTTP_VERSION_1_1);
    curl_easy_setopt(easy, CURLOPT_HTTPPROXYTUNNEL, 1L);
    curl_easy_setopt(easy, CURLOPT_USERAGENT, USER_AGENT);
    curl_easy_setopt(easy, CURLOPT_FOLLOWLOCATION, 0L);
    curl_easy_setopt(easy, CURLOPT_NOSIGNAL, 1L);
    curl_easy_setopt(easy, CURLOPT_TCP_NODELAY, 1L);
    curl_easy_setopt(easy, CURLOPT_TCP_KEEPALIVE, 1L);
    curl_easy_setopt(easy, CURLOPT_CONNECTTIMEOUT_MS, 30000L);
    curl_easy_setopt(easy, CURLOPT_TIMEOUT_MS,
                     session->client->request_timeout_ms);
    curl_easy_setopt(easy, CURLOPT_XFERINFOFUNCTION, transfer_progress);
    curl_easy_setopt(easy, CURLOPT_XFERINFODATA, session->client);
    curl_easy_setopt(easy, CURLOPT_NOPROGRESS, 0L);

    CURLcode code = curl_easy_perform(easy);
    if (code != CURLE_OK) {
        char message[MAX_ERROR_LEN];
        snprintf(message, sizeof(message), "WebSocket connection failed: %s",
                 curl_easy_strerror(code));
        websocket_ping_session_cleanup(session);
        set_error(session, message);
        return code == CURLE_OPERATION_TIMEDOUT ? ERR_TIMEOUT : ERR_NETWORK;
    }

    unsigned char key_bytes[16];
    random_bytes(key_bytes, sizeof(key_bytes));
    char key[32];
    if (!base64_encode(key_bytes, sizeof(key_bytes), key, sizeof(key))) {
        websocket_ping_session_cleanup(session);
        set_error(session, "failed to encode WebSocket handshake key");
        return ERR_PROTOCOL;
    }
    char accept_source[128];
    int source_length = snprintf(accept_source, sizeof(accept_source), "%s%s",
                                 key, WEBSOCKET_GUID);
    if (source_length < 0 || (size_t)source_length >= sizeof(accept_source)) {
        websocket_ping_session_cleanup(session);
        set_error(session, "failed to construct WebSocket acceptance proof");
        return ERR_PROTOCOL;
    }
    sha1_context_t sha1;
    unsigned char accept_digest[20];
    char expected_accept[32];
    sha1_init(&sha1);
    sha1_update(&sha1, (const unsigned char *)accept_source,
                (size_t)source_length);
    sha1_final(&sha1, accept_digest);
    if (!base64_encode(accept_digest, sizeof(accept_digest),
                       expected_accept, sizeof(expected_accept))) {
        websocket_ping_session_cleanup(session);
        set_error(session, "failed to encode WebSocket acceptance proof");
        return ERR_PROTOCOL;
    }

    char request[MAX_URL_LEN * 3 + MAX_TOKEN_LEN + 1024];
    int request_length;
    if (session->client->access_token[0]) {
        request_length = snprintf(
            request, sizeof(request),
            "GET %s HTTP/1.1\r\n"
            "Host: %s\r\n"
            "User-Agent: %s\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "Sec-WebSocket-Key: %s\r\n"
            "Sec-WebSocket-Protocol: %s\r\n"
            "Authorization: Bearer %s\r\n"
            "Accept-Encoding: identity\r\n"
            "Cache-Control: no-store, no-transform\r\n"
            "Pragma: no-cache\r\n\r\n",
            request_target, authority, USER_AGENT, key,
            NETSPEED_WEBSOCKET_PING_PROTOCOL,
            session->client->access_token);
    } else {
        request_length = snprintf(
            request, sizeof(request),
            "GET %s HTTP/1.1\r\n"
            "Host: %s\r\n"
            "User-Agent: %s\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "Sec-WebSocket-Key: %s\r\n"
            "Sec-WebSocket-Protocol: %s\r\n"
            "Accept-Encoding: identity\r\n"
            "Cache-Control: no-store, no-transform\r\n"
            "Pragma: no-cache\r\n\r\n",
            request_target, authority, USER_AGENT, key,
            NETSPEED_WEBSOCKET_PING_PROTOCOL);
    }
    if (request_length < 0 || (size_t)request_length >= sizeof(request)) {
        websocket_ping_session_cleanup(session);
        set_error(session, "WebSocket upgrade request exceeded the safe size limit");
        return ERR_ARGS;
    }

    int64_t deadline = timing_monotonic_ms() +
                       session->client->request_timeout_ms;
    int status = send_all(session, (const unsigned char *)request,
                          (size_t)request_length, deadline);
    if (status != ERR_OK) {
        char message[MAX_ERROR_LEN];
        snprintf(message, sizeof(message), "%s",
                 websocket_ping_error(session));
        websocket_ping_session_cleanup(session);
        set_error(session, message);
        return status;
    }

    char response[WEBSOCKET_HANDSHAKE_LIMIT + 1U];
    size_t response_length = 0;
    char *terminator = NULL;
    while (!terminator) {
        if (response_length == WEBSOCKET_HANDSHAKE_LIMIT) {
            websocket_ping_session_cleanup(session);
            set_error(session,
                      "WebSocket upgrade response exceeded the header limit");
            return ERR_PROTOCOL;
        }
        size_t received = 0;
        status = receive_some(session,
                              (unsigned char *)response + response_length,
                              WEBSOCKET_HANDSHAKE_LIMIT - response_length,
                              &received, deadline);
        if (status != ERR_OK) {
            char message[MAX_ERROR_LEN];
            snprintf(message, sizeof(message), "%s",
                     websocket_ping_error(session));
            websocket_ping_session_cleanup(session);
            set_error(session, message);
            return status;
        }
        response_length += received;
        response[response_length] = '\0';
        terminator = strstr(response, "\r\n\r\n");
    }
    size_t header_length = (size_t)(terminator - response) + 4U;
    if (header_length != response_length) {
        websocket_ping_session_cleanup(session);
        set_error(session,
                  "WebSocket peer sent data before the upgrade completed");
        return ERR_PROTOCOL;
    }

    handshake_headers_t headers;
#define REJECT_HANDSHAKE(message)                                                \
    do {                                                                         \
        websocket_ping_session_cleanup(session);                                 \
        set_error(session, (message));                                            \
        return ERR_PROTOCOL;                                                      \
    } while (0)
    if (!parse_handshake_response(response, &headers) || headers.overflow) {
        REJECT_HANDSHAKE("WebSocket upgrade response contained malformed or oversized headers");
    }
    if (headers.status != 101) {
        REJECT_HANDSHAKE("WebSocket upgrade response did not return HTTP 101");
    }
    if (!header_has_token(headers.upgrade, "websocket") ||
        !header_has_token(headers.connection, "upgrade")) {
        REJECT_HANDSHAKE("WebSocket upgrade response omitted required Upgrade headers");
    }
    if (headers.accept_count != 1 || headers.accept_conflict ||
        strcmp(headers.accept, expected_accept) != 0) {
        REJECT_HANDSHAKE("WebSocket upgrade response returned an invalid Sec-WebSocket-Accept");
    }
    if (headers.protocol_count != 1 || headers.protocol_conflict ||
        strcmp(headers.protocol, NETSPEED_WEBSOCKET_PING_PROTOCOL) != 0) {
        REJECT_HANDSHAKE("WebSocket upgrade response selected an invalid subprotocol");
    }
    if (!cache_has_directive(headers.cache_control, "no-store") ||
        !cache_has_directive(headers.cache_control, "no-transform")) {
        REJECT_HANDSHAKE("WebSocket upgrade response omitted Cache-Control: no-store, no-transform");
    }
    if (!identity_encoding_only(headers.content_encoding)) {
        REJECT_HANDSHAKE("WebSocket upgrade response used a non-identity Content-Encoding");
    }
    if (!all_header_values_equal(headers.x_accel_buffering, "no")) {
        REJECT_HANDSHAKE("WebSocket upgrade response did not suppress proxy buffering");
    }
    if (!all_header_values_equal(headers.measurement, "latency")) {
        REJECT_HANDSHAKE("WebSocket upgrade response did not identify a latency measurement");
    }
#undef REJECT_HANDSHAKE

    session->connected = true;
    return ERR_OK;
}

int websocket_ping_measure(websocket_ping_session_t *session,
                           uint32_t sequence,
                           double *rtt_ms)
{
    if (!session || !session->connected || !session->easy || !rtt_ms) {
        if (session) {
            set_error(session, "WebSocket latency connection is not open");
        }
        return ERR_ARGS;
    }
    unsigned char transmitted[NETSPEED_WEBSOCKET_PING_PAYLOAD_BYTES];
    build_payload(sequence, transmitted);
    int64_t deadline = timing_monotonic_ms() +
                       session->client->request_timeout_ms;
    struct timespec started;
    timing_now(&started);
    int status = send_frame(session, 0x2U, transmitted,
                            sizeof(transmitted), deadline);
    if (status != ERR_OK) {
        return status;
    }

    for (;;) {
        unsigned char opcode = 0;
        unsigned char received[WEBSOCKET_FRAME_LIMIT];
        size_t received_length = 0;
        status = receive_frame(session, &opcode, received, sizeof(received),
                               &received_length, deadline);
        if (status != ERR_OK) {
            return status;
        }
        if (opcode == 0x9U) {
            status = send_frame(session, 0xAU, received, received_length,
                                deadline);
            if (status != ERR_OK) return status;
            continue;
        }
        if (opcode == 0xAU) {
            continue;
        }
        if (opcode == 0x8U) {
            set_error(session, "WebSocket peer closed the latency connection");
            return ERR_NETWORK;
        }
        if (opcode != 0x2U) {
            set_error(session,
                      "WebSocket latency response used an unsupported opcode");
            return ERR_PROTOCOL;
        }
        struct timespec ended;
        timing_now(&ended);
        if (received_length != sizeof(transmitted) ||
            !payload_equal(transmitted, received, sizeof(transmitted))) {
            set_error(session,
                      "WebSocket latency echo did not match the transmitted nonce");
            return ERR_PROTOCOL;
        }
        *rtt_ms = timing_diff_ms(&started, &ended);
        if (!(*rtt_ms > 0)) {
            set_error(session, "WebSocket latency duration was not positive");
            return ERR_PROTOCOL;
        }
        return ERR_OK;
    }
}
