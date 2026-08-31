#define _POSIX_C_SOURCE 200809L
#include "cloudflare.h"

#include <curl/curl.h>
#include <ctype.h>
#include <errno.h>
#include <inttypes.h>
#include <limits.h>
#include <math.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <time.h>
#include <unistd.h>
#include <zlib.h>

#ifdef NS_HAVE_LIBDATACHANNEL
#include <rtc/rtc.h>
#endif

#define CF_TRANSPORT_PROBE_BYTES (64U * 1024U)
#define CF_CAPTURE_BUDGET (64U * 1024U)
#define CF_CAPTURE_RANGES 4
#define CF_MAX_PROTOCOLS 4
#define CF_MAX_RESPONSE_BODY (1024U * 1024U)
#define CF_LATENCY_ATTEMPTS 4
#define CF_SERVER_TIMING_MIN_MS 0.01

typedef struct {
    const char *provider;
    const char *server;
    const char *token;
    const char *turn_credentials_url;
    const char *turn_url;
    const char *turn_username;
    const char *turn_credential;
    const char *download_payload;
    const char *download_framing;
    const char *download_flush;
    size_t download_chunk_bytes;
    bool provider_explicit;
    bool json;
    bool quiet;
    bool csv;
    bool quick;
    bool download_only;
    bool upload_only;
    bool skip_packet;
    bool insecure;
} cf_options;

typedef struct {
    char *data;
    size_t len;
    size_t cap;
} buffer;

typedef struct {
    double *v;
    size_t n;
    size_t cap;
} doubles;

typedef struct {
    uint64_t start;
    size_t length;
    size_t destination;
    size_t filled;
} capture_range;

typedef struct {
    uint64_t expected;
    uint64_t offset;
    unsigned char data[CF_CAPTURE_BUDGET];
    capture_range ranges[CF_CAPTURE_RANGES];
    size_t range_count;
    size_t captured;
} distributed_capture;

typedef struct {
    long status;
    int64_t content_length;
    int64_t chunk_bytes;
    bool chunk_bytes_header_present;
    bool invalid_chunk_bytes_header;
    bool flush_present;
    bool flush;
    bool invalid_flush_header;
    char content_encoding[64];
    char cache_control[256];
    char transfer_encoding[128];
    char x_accel_buffering[64];
    char payload[32];
    char framing[32];
    char server_timing[1024];
    char http_protocol[16];
    bool cloudflare_fingerprint;
    uint64_t body_bytes;
    long new_connections;
    bool connection_reused;
    double request_to_first_byte_ms;
    double total_ms;
} cf_response;

typedef struct {
    CURL *easy;
    const cf_options *options;
    char error[CURL_ERROR_SIZE];
} cf_http_session;

typedef struct {
    buffer *buffer_body;
    size_t buffer_limit;
    distributed_capture *capture;
    int64_t exact_body_bytes;
    uint64_t received;
    bool overflow;
} cf_write_state;

typedef struct {
    cf_response *response;
} cf_header_state;

typedef struct {
    uint64_t remaining;
    uint64_t read;
} upload_src;

typedef struct {
    char capability_source[32];
    bool provider_defaults_only;
    bool query_discriminators_sent;
    char download_path[32];
    char upload_path[32];
    char latency_path[32];
    char bytes_parameter[16];
    char upload_payload[24];

    char requested_payload[16];
    char selected_payload[16];
    char payload_evidence[192];
    char requested_framing[16];
    char selected_framing[16];
    char framing_evidence[192];
    size_t requested_chunk_bytes;
    bool chunk_bytes_present;
    size_t chunk_bytes;
    char chunk_evidence[96];
    char requested_flush[16];
    bool flush_present;
    bool flush;
    char flush_evidence[96];

    bool transport_compression_disabled;
    char request_accept_encoding[32];
    char request_cache_control[64];
    char request_pragma[32];
    char upload_content_encoding[32];
    char response_content_encoding[64];
    bool response_no_store;
    bool response_no_transform;
    bool proxy_buffer_suppression_observed;
} cf_transport;

typedef struct {
    bool available;
    double mbps;
    doubles samples;
    const char *evidence;
    char error[256];
} speed_result;

typedef struct {
    bool available;
    double median;
    double p90;
    double jitter;
    doubles samples;
    bool connection_reused;
    int warm_samples;
    int warmup_requests;
    int discarded_cold_attempts;
    int server_timing_adjusted_samples;
    char protocols[CF_MAX_PROTOCOLS][16];
    int protocol_count;
    char error[256];
} latency_result;

typedef struct {
    bool available;
    int sent;
    int received;
    int lost;
    double loss;
    char reason[192];
} packet_result;

typedef struct {
    const cf_options *options;
    const cf_transport *transport;
    bool upload;
    size_t bytes;
    double until;
    uint64_t completed;
    doubles samples;
    pthread_mutex_t mutex;
    bool failed;
    char error[256];
} load_state;

typedef struct {
    const cf_options *options;
    const cf_transport *transport;
    cf_http_session http;
    int warmup_requests;
    int discarded_cold_attempts;
    int server_timing_adjusted_samples;
    char protocols[CF_MAX_PROTOCOLS][16];
    int protocol_count;
} cf_latency_session;

enum {
    CF_OK = 0,
    CF_ERROR = -1,
    CF_CONTROL_ERROR = -2,
    CF_PROTOCOL_ERROR = -3
};

static double mono(void)
{
    struct timespec ts;
    if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
        return 0;
    }
    return (double)ts.tv_sec + (double)ts.tv_nsec / 1e9;
}

static void sleep_us(long microseconds)
{
    struct timespec ts;
    ts.tv_sec = microseconds / 1000000;
    ts.tv_nsec = (microseconds % 1000000) * 1000;
    while (nanosleep(&ts, &ts) != 0 && errno == EINTR) {
    }
}

static int dpush(doubles *values, double value)
{
    if (values->n == values->cap) {
        size_t capacity = values->cap ? values->cap * 2 : 16;
        double *next = realloc(values->v, capacity * sizeof(*next));
        if (!next) {
            return -1;
        }
        values->v = next;
        values->cap = capacity;
    }
    values->v[values->n++] = value;
    return 0;
}

static int compare_double(const void *left, const void *right)
{
    double a = *(const double *)left;
    double b = *(const double *)right;
    return (a > b) - (a < b);
}

static double percentile(const doubles *values, double fraction)
{
    if (!values->n) {
        return NAN;
    }
    double *sorted = malloc(values->n * sizeof(*sorted));
    if (!sorted) {
        return NAN;
    }
    memcpy(sorted, values->v, values->n * sizeof(*sorted));
    qsort(sorted, values->n, sizeof(*sorted), compare_double);
    double position = fraction * (double)(values->n - 1);
    double floor_position = floor(position);
    double ceil_position = ceil(position);
    double result = sorted[(size_t)floor_position];
    if (ceil_position != floor_position) {
        result += (sorted[(size_t)ceil_position] - result) *
                  (position - floor_position);
    }
    free(sorted);
    return result;
}

static bool contains_ci(const char *haystack, const char *needle)
{
    if (!haystack || !needle) {
        return false;
    }
    size_t needle_length = strlen(needle);
    if (!needle_length) {
        return true;
    }
    for (; *haystack; haystack++) {
        size_t index = 0;
        while (index < needle_length && haystack[index] &&
               tolower((unsigned char)haystack[index]) ==
                   tolower((unsigned char)needle[index])) {
            index++;
        }
        if (index == needle_length) {
            return true;
        }
    }
    return false;
}

static bool header_has_directive(const char *value, const char *target)
{
    if (!value || !target) {
        return false;
    }
    size_t target_length = strlen(target);
    const char *cursor = value;
    while (*cursor) {
        while (*cursor == ',' || isspace((unsigned char)*cursor)) {
            cursor++;
        }
        const char *end = cursor;
        while (*end && *end != ',') {
            end++;
        }
        const char *trimmed_end = end;
        while (trimmed_end > cursor &&
               isspace((unsigned char)trimmed_end[-1])) {
            trimmed_end--;
        }
        if ((size_t)(trimmed_end - cursor) == target_length &&
            strncasecmp(cursor, target, target_length) == 0) {
            return true;
        }
        cursor = end;
    }
    return false;
}

static bool host_is_cloudflare(const char *server)
{
    return server &&
           (contains_ci(server, "speed.cloudflare.com") ||
            contains_ci(server, ".cloudflare.com") ||
            contains_ci(server, ".cloudflare.net"));
}

static bool has_netspeed_marker(const char *document)
{
    return document &&
           (strstr(document, "measurementProtocolVersion") ||
            strstr(document, "measurementApiVersion") ||
            strstr(document, "uploadReceiptVersion") ||
            strstr(document, "maxTransferBytes"));
}

static char *join_url(const char *base, const char *path, const char *query)
{
    size_t needed = strlen(base) + strlen(path) +
                    (query ? strlen(query) : 0) + 4;
    char *output = malloc(needed);
    if (!output) {
        return NULL;
    }
    size_t base_length = strlen(base);
    snprintf(output, needed, "%s%s%s%s", base,
             base_length && base[base_length - 1] == '/' ? "" : "/",
             path[0] == '/' ? path + 1 : path, query ? query : "");
    return output;
}

static int buffer_append(buffer *output, const void *data, size_t length,
                         size_t limit)
{
    if (!output || length > limit || output->len > limit - length) {
        return -1;
    }
    size_t needed = output->len + length + 1;
    if (needed > output->cap) {
        size_t capacity = output->cap ? output->cap * 2 : 4096;
        while (capacity < needed) {
            if (capacity > limit / 2) {
                capacity = limit + 1;
                break;
            }
            capacity *= 2;
        }
        char *next = realloc(output->data, capacity);
        if (!next) {
            return -1;
        }
        output->data = next;
        output->cap = capacity;
    }
    memcpy(output->data + output->len, data, length);
    output->len += length;
    output->data[output->len] = '\0';
    return 0;
}

#ifdef NS_HAVE_LIBDATACHANNEL
static size_t write_buf(char *pointer, size_t size, size_t count, void *opaque)
{
    buffer *output = opaque;
    if (size && count > SIZE_MAX / size) {
        return 0;
    }
    size_t length = size * count;
    return buffer_append(output, pointer, length, CF_MAX_RESPONSE_BODY) == 0
               ? length
               : 0;
}
#endif

static void capture_init(distributed_capture *capture, uint64_t expected)
{
    memset(capture, 0, sizeof(*capture));
    capture->expected = expected;
    if (expected == 0) {
        return;
    }
    if (expected <= CF_CAPTURE_BUDGET) {
        capture->range_count = 1;
        capture->ranges[0].start = 0;
        capture->ranges[0].length =
            (size_t)(expected < CF_CAPTURE_BUDGET ? expected : CF_CAPTURE_BUDGET);
        return;
    }

    size_t window = CF_CAPTURE_BUDGET / CF_CAPTURE_RANGES;
    uint64_t centers[CF_CAPTURE_RANGES] = {
        0,
        expected / 3,
        (expected * 2) / 3,
        expected - window,
    };
    for (size_t index = 0; index < CF_CAPTURE_RANGES; index++) {
        uint64_t start = centers[index];
        if (index == 1 || index == 2) {
            start = start > window / 2 ? start - window / 2 : 0;
        }
        if (start + window > expected) {
            start = expected - window;
        }
        capture->ranges[index].start = start;
        capture->ranges[index].length = window;
        capture->ranges[index].destination = index * window;
    }
    capture->range_count = CF_CAPTURE_RANGES;
}

static void capture_feed(distributed_capture *capture, const unsigned char *data,
                         size_t length)
{
    if (!capture || !length) {
        return;
    }
    uint64_t chunk_start = capture->offset;
    uint64_t chunk_end = chunk_start + length;
    for (size_t index = 0; index < capture->range_count; index++) {
        capture_range *range = &capture->ranges[index];
        uint64_t range_start = range->start;
        uint64_t range_end = range_start + range->length;
        uint64_t overlap_start = chunk_start > range_start ? chunk_start : range_start;
        uint64_t overlap_end = chunk_end < range_end ? chunk_end : range_end;
        if (overlap_start >= overlap_end) {
            continue;
        }
        size_t source_offset = (size_t)(overlap_start - chunk_start);
        size_t range_offset = (size_t)(overlap_start - range_start);
        size_t copy_length = (size_t)(overlap_end - overlap_start);
        memcpy(capture->data + range->destination + range_offset,
               data + source_offset, copy_length);
        range->filled += copy_length;
        capture->captured += copy_length;
    }
    capture->offset = chunk_end;
}

static bool capture_complete(const distributed_capture *capture)
{
    if (!capture) {
        return false;
    }
    for (size_t index = 0; index < capture->range_count; index++) {
        if (capture->ranges[index].filled != capture->ranges[index].length) {
            return false;
        }
    }
    return true;
}

static size_t capture_compact(distributed_capture *capture)
{
    size_t destination = 0;
    for (size_t index = 0; index < capture->range_count; index++) {
        capture_range *range = &capture->ranges[index];
        if (range->destination != destination) {
            memmove(capture->data + destination,
                    capture->data + range->destination, range->length);
        }
        destination += range->length;
    }
    return destination;
}

static void response_init(cf_response *response)
{
    memset(response, 0, sizeof(*response));
    response->content_length = -1;
    response->chunk_bytes = -1;
    response->new_connections = -1;
    snprintf(response->http_protocol, sizeof(response->http_protocol), "%s",
             "unknown");
}

static char *trim_header_value(char *value)
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

static bool append_header_value(char *destination, size_t capacity,
                                const char *value)
{
    if (!destination || capacity == 0 || !value) {
        return false;
    }
    size_t length = strnlen(destination, capacity);
    if (length >= capacity) {
        return false;
    }
    size_t separator = length ? 2 : 0;
    size_t value_length = strlen(value);
    if (separator > capacity - 1 - length ||
        value_length > capacity - 1 - length - separator) {
        return false;
    }
    if (separator) {
        destination[length++] = ',';
        destination[length++] = ' ';
    }
    memcpy(destination + length, value, value_length + 1);
    return true;
}

static bool parse_nonnegative_int64(const char *value, int64_t *destination)
{
    errno = 0;
    char *end = NULL;
    long long parsed = strtoll(value, &end, 10);
    if (errno != 0 || !end || *end != '\0' || parsed < 0) {
        return false;
    }
    *destination = (int64_t)parsed;
    return true;
}

static size_t cf_header_callback(char *pointer, size_t size, size_t count,
                                 void *opaque)
{
    cf_header_state *state = opaque;
    if (size && count > SIZE_MAX / size) {
        return 0;
    }
    size_t length = size * count;
    if (!length) {
        return 0;
    }
    if (length >= 4096) {
        return 0;
    }
    char line[4096];
    memcpy(line, pointer, length);
    line[length] = '\0';
    char *colon = strchr(line, ':');
    if (!colon) {
        return length;
    }
    *colon = '\0';
    char *value = trim_header_value(colon + 1);
    cf_response *response = state->response;
    if (strcasecmp(line, "Content-Length") == 0) {
        (void)parse_nonnegative_int64(value, &response->content_length);
    } else if (strcasecmp(line, "Content-Encoding") == 0) {
        if (!append_header_value(response->content_encoding,
                                 sizeof(response->content_encoding), value)) {
            return 0;
        }
    } else if (strcasecmp(line, "Cache-Control") == 0) {
        if (!append_header_value(response->cache_control,
                                 sizeof(response->cache_control), value)) {
            return 0;
        }
    } else if (strcasecmp(line, "Transfer-Encoding") == 0) {
        if (!append_header_value(response->transfer_encoding,
                                 sizeof(response->transfer_encoding), value)) {
            return 0;
        }
    } else if (strcasecmp(line, "X-Accel-Buffering") == 0) {
        snprintf(response->x_accel_buffering,
                 sizeof(response->x_accel_buffering), "%s", value);
    } else if (strcasecmp(line, "X-Netspeed-Payload") == 0) {
        snprintf(response->payload, sizeof(response->payload), "%s", value);
    } else if (strcasecmp(line, "X-Netspeed-Framing") == 0) {
        snprintf(response->framing, sizeof(response->framing), "%s", value);
    } else if (strcasecmp(line, "X-Netspeed-Chunk-Bytes") == 0) {
        response->chunk_bytes_header_present = true;
        if (!parse_nonnegative_int64(value, &response->chunk_bytes) ||
            response->chunk_bytes == 0) {
            response->invalid_chunk_bytes_header = true;
        }
    } else if (strcasecmp(line, "X-Netspeed-Flush") == 0) {
        if (strcasecmp(value, "true") == 0) {
            response->flush_present = true;
            response->flush = true;
        } else if (strcasecmp(value, "false") == 0) {
            response->flush_present = true;
            response->flush = false;
        } else {
            response->invalid_flush_header = true;
        }
    } else if (strcasecmp(line, "Server-Timing") == 0) {
        if (!append_header_value(response->server_timing,
                                 sizeof(response->server_timing), value)) {
            return 0;
        }
    }
    if (strncasecmp(line, "CF-", 3) == 0 ||
        (strcasecmp(line, "Server") == 0 && contains_ci(value, "cloudflare"))) {
        response->cloudflare_fingerprint = true;
    }
    return length;
}

static size_t cf_write_callback(char *pointer, size_t size, size_t count,
                                void *opaque)
{
    cf_write_state *state = opaque;
    if (size && count > SIZE_MAX / size) {
        state->overflow = true;
        return 0;
    }
    size_t length = size * count;
    if (!length) {
        return 0;
    }
    if (state->received > UINT64_MAX - length) {
        state->overflow = true;
        return 0;
    }
    state->received += length;
    if (state->exact_body_bytes >= 0 &&
        state->received > (uint64_t)state->exact_body_bytes) {
        state->overflow = true;
        return 0;
    }
    if (state->exact_body_bytes < 0 && state->buffer_limit > 0 &&
        state->received > state->buffer_limit) {
        state->overflow = true;
        return 0;
    }
    if (state->buffer_body &&
        buffer_append(state->buffer_body, pointer, length,
                      state->buffer_limit) != 0) {
        state->overflow = true;
        return 0;
    }
    capture_feed(state->capture, (const unsigned char *)pointer, length);
    return length;
}

static size_t read_ascii_zero(char *pointer, size_t size, size_t count,
                              void *opaque)
{
    upload_src *source = opaque;
    if (size && count > SIZE_MAX / size) {
        return CURL_READFUNC_ABORT;
    }
    size_t capacity = size * count;
    if (!source->remaining || !capacity) {
        return 0;
    }
    if ((uint64_t)capacity > source->remaining) {
        capacity = (size_t)source->remaining;
    }
    memset(pointer, '0', capacity);
    source->remaining -= capacity;
    source->read += capacity;
    return capacity;
}

static struct curl_slist *append_header(struct curl_slist *headers,
                                        const char *value, bool *ok)
{
    struct curl_slist *next = curl_slist_append(headers, value);
    if (!next) {
        *ok = false;
        curl_slist_free_all(headers);
        return NULL;
    }
    return next;
}

static struct curl_slist *measurement_headers(const cf_options *options,
                                              bool upload)
{
    struct curl_slist *headers = NULL;
    bool ok = true;
    headers = append_header(headers, "Accept: */*", &ok);
    if (!ok) return NULL;
    headers = append_header(headers, "Accept-Encoding: identity", &ok);
    if (!ok) return NULL;
    headers = append_header(headers, "Cache-Control: no-store, no-transform", &ok);
    if (!ok) return NULL;
    headers = append_header(headers, "Pragma: no-cache", &ok);
    if (!ok) return NULL;
    headers = append_header(headers, "Expect:", &ok);
    if (!ok) return NULL;
    if (upload) {
        headers = append_header(headers, "Content-Type: application/octet-stream", &ok);
        if (!ok) return NULL;
        headers = append_header(headers, "Content-Encoding: identity", &ok);
        if (!ok) return NULL;
    }
    if (options->token && *options->token) {
        size_t needed = strlen(options->token) + 32;
        char *authorization = malloc(needed);
        if (!authorization) {
            curl_slist_free_all(headers);
            return NULL;
        }
        snprintf(authorization, needed, "Authorization: Bearer %s",
                 options->token);
        headers = append_header(headers, authorization, &ok);
        free(authorization);
        if (!ok) return NULL;
    }
    return headers;
}

static void cf_http_session_init(cf_http_session *session,
                                 const cf_options *options)
{
    memset(session, 0, sizeof(*session));
    session->options = options;
    session->easy = curl_easy_init();
}

static void cf_http_session_cleanup(cf_http_session *session)
{
    if (session && session->easy) {
        curl_easy_cleanup(session->easy);
    }
    if (session) {
        memset(session, 0, sizeof(*session));
    }
}

static const char *http_protocol_name(long version)
{
    switch (version) {
    case CURL_HTTP_VERSION_1_0:
        return "HTTP/1.0";
    case CURL_HTTP_VERSION_1_1:
        return "HTTP/1.1";
#ifdef CURL_HTTP_VERSION_2_0
    case CURL_HTTP_VERSION_2_0:
        return "HTTP/2";
#endif
#ifdef CURL_HTTP_VERSION_3
    case CURL_HTTP_VERSION_3:
        return "HTTP/3";
#endif
    default:
        return "unknown";
    }
}

static int cf_http_request(cf_http_session *session, const char *method,
                           const char *path, const char *query,
                           uint64_t upload_bytes, int64_t exact_body_bytes,
                           buffer *body, size_t body_limit,
                           distributed_capture *capture,
                           cf_response *response)
{
    if (!session || !session->easy || !session->options || !method ||
        !path || !response) {
        return CF_ERROR;
    }
    response_init(response);
    char *url = join_url(session->options->server, path, query);
    if (!url) {
        snprintf(session->error, sizeof(session->error), "%s",
                 "request URL allocation failed");
        return CF_ERROR;
    }
    bool upload = strcmp(method, "POST") == 0;
    struct curl_slist *headers = measurement_headers(session->options, upload);
    if (!headers) {
        free(url);
        snprintf(session->error, sizeof(session->error), "%s",
                 "request header allocation failed");
        return CF_ERROR;
    }

    cf_write_state write_state = {
        .buffer_body = body,
        .buffer_limit = body_limit,
        .capture = capture,
        .exact_body_bytes = exact_body_bytes,
    };
    cf_header_state header_state = {.response = response};
    upload_src upload_source = {.remaining = upload_bytes};

    CURL *easy = session->easy;
    curl_easy_reset(easy);
    memset(session->error, 0, sizeof(session->error));
    curl_easy_setopt(easy, CURLOPT_ERRORBUFFER, session->error);
    curl_easy_setopt(easy, CURLOPT_URL, url);
    curl_easy_setopt(easy, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(easy, CURLOPT_FOLLOWLOCATION, 0L);
    curl_easy_setopt(easy, CURLOPT_NOSIGNAL, 1L);
    curl_easy_setopt(easy, CURLOPT_TIMEOUT, 30L);
    curl_easy_setopt(easy, CURLOPT_CONNECTTIMEOUT, 15L);
    curl_easy_setopt(easy, CURLOPT_TCP_NODELAY, 1L);
    curl_easy_setopt(easy, CURLOPT_TCP_KEEPALIVE, 1L);
    curl_easy_setopt(easy, CURLOPT_FORBID_REUSE, 0L);
    curl_easy_setopt(easy, CURLOPT_FRESH_CONNECT, 0L);
    curl_easy_setopt(easy, CURLOPT_MAXCONNECTS, 1L);
    curl_easy_setopt(easy, CURLOPT_ACCEPT_ENCODING, "identity");
    curl_easy_setopt(easy, CURLOPT_HTTP_CONTENT_DECODING, 0L);
    curl_easy_setopt(easy, CURLOPT_SSL_VERIFYPEER,
                     session->options->insecure ? 0L : 1L);
    curl_easy_setopt(easy, CURLOPT_SSL_VERIFYHOST,
                     session->options->insecure ? 0L : 2L);
    curl_easy_setopt(easy, CURLOPT_WRITEFUNCTION, cf_write_callback);
    curl_easy_setopt(easy, CURLOPT_WRITEDATA, &write_state);
    curl_easy_setopt(easy, CURLOPT_HEADERFUNCTION, cf_header_callback);
    curl_easy_setopt(easy, CURLOPT_HEADERDATA, &header_state);

    if (upload) {
        curl_easy_setopt(easy, CURLOPT_POST, 1L);
        curl_easy_setopt(easy, CURLOPT_READFUNCTION, read_ascii_zero);
        curl_easy_setopt(easy, CURLOPT_READDATA, &upload_source);
        curl_easy_setopt(easy, CURLOPT_POSTFIELDSIZE_LARGE,
                         (curl_off_t)upload_bytes);
    } else {
        curl_easy_setopt(easy, CURLOPT_HTTPGET, 1L);
    }

    CURLcode curl_status = curl_easy_perform(easy);
    double pretransfer = 0;
    double first_byte = 0;
    double total = 0;
    curl_easy_getinfo(easy, CURLINFO_PRETRANSFER_TIME, &pretransfer);
    curl_easy_getinfo(easy, CURLINFO_STARTTRANSFER_TIME, &first_byte);
    curl_easy_getinfo(easy, CURLINFO_TOTAL_TIME, &total);
    response->request_to_first_byte_ms = (first_byte - pretransfer) * 1000.0;
    response->total_ms = total * 1000.0;
    curl_easy_getinfo(easy, CURLINFO_RESPONSE_CODE, &response->status);
    curl_easy_getinfo(easy, CURLINFO_NUM_CONNECTS, &response->new_connections);
    response->connection_reused = response->new_connections == 0;
    long http_version = 0;
    if (curl_easy_getinfo(easy, CURLINFO_HTTP_VERSION, &http_version) == CURLE_OK) {
        snprintf(response->http_protocol, sizeof(response->http_protocol), "%s",
                 http_protocol_name(http_version));
    }
    if (response->content_length < 0) {
        curl_off_t content_length = -1;
        if (curl_easy_getinfo(easy, CURLINFO_CONTENT_LENGTH_DOWNLOAD_T,
                             &content_length) == CURLE_OK &&
            content_length >= 0) {
            response->content_length = (int64_t)content_length;
        }
    }
    response->body_bytes = write_state.received;

    curl_slist_free_all(headers);
    free(url);
    if (curl_status != CURLE_OK || write_state.overflow) {
        if (!session->error[0]) {
            snprintf(session->error, sizeof(session->error), "%s",
                     write_state.overflow ? "response violated body-size contract"
                                          : curl_easy_strerror(curl_status));
        }
        return CF_ERROR;
    }
    if (upload_source.read != upload_bytes) {
        snprintf(session->error, sizeof(session->error),
                 "upload source produced %" PRIu64 " of %" PRIu64 " bytes",
                 upload_source.read, upload_bytes);
        return CF_PROTOCOL_ERROR;
    }
    return CF_OK;
}

#ifdef NS_HAVE_LIBDATACHANNEL
/* Arbitrary absolute-URL GET used only by TURN credential discovery. */
static int http_get_buffer(const cf_options *options, const char *url,
                           buffer *body, long *status, char **headers_output)
{
    CURL *easy = curl_easy_init();
    if (!easy) {
        return -1;
    }
    struct curl_slist *headers = measurement_headers(options, false);
    if (!headers) {
        curl_easy_cleanup(easy);
        return -1;
    }
    buffer header_buffer = {0};
    curl_easy_setopt(easy, CURLOPT_URL, url);
    curl_easy_setopt(easy, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(easy, CURLOPT_FOLLOWLOCATION, 0L);
    curl_easy_setopt(easy, CURLOPT_NOSIGNAL, 1L);
    curl_easy_setopt(easy, CURLOPT_TIMEOUT, 30L);
    curl_easy_setopt(easy, CURLOPT_ACCEPT_ENCODING, "identity");
    curl_easy_setopt(easy, CURLOPT_HTTP_CONTENT_DECODING, 0L);
    curl_easy_setopt(easy, CURLOPT_SSL_VERIFYPEER, options->insecure ? 0L : 1L);
    curl_easy_setopt(easy, CURLOPT_SSL_VERIFYHOST, options->insecure ? 0L : 2L);
    curl_easy_setopt(easy, CURLOPT_WRITEFUNCTION, write_buf);
    curl_easy_setopt(easy, CURLOPT_WRITEDATA, body);
    if (headers_output) {
        curl_easy_setopt(easy, CURLOPT_HEADERFUNCTION, write_buf);
        curl_easy_setopt(easy, CURLOPT_HEADERDATA, &header_buffer);
    }
    CURLcode result = curl_easy_perform(easy);
    curl_easy_getinfo(easy, CURLINFO_RESPONSE_CODE, status);
    curl_slist_free_all(headers);
    curl_easy_cleanup(easy);
    if (headers_output) {
        *headers_output = header_buffer.data;
    } else {
        free(header_buffer.data);
    }
    return result == CURLE_OK ? 0 : -1;
}
#endif

static int parse_size_value(const char *text, size_t *value)
{
    if (!text || !*text || *text == '-') {
        return -1;
    }
    errno = 0;
    char *end = NULL;
    unsigned long long parsed = strtoull(text, &end, 10);
    if (errno != 0 || !end || *end != '\0' || parsed > INT_MAX) {
        return -1;
    }
    *value = (size_t)parsed;
    return 0;
}

static int parse_provider(cf_options *options, int *argc, char ***argv)
{
    char **arguments = *argv;
    int output = 1;
    bool server_explicit = false;
    bool positional_server = false;

    options->provider = "auto";
    options->server = "http://localhost:8080";
    options->download_payload = "auto";
    options->download_framing = "auto";
    options->download_flush = "auto";

    for (int index = 1; index < *argc; index++) {
        char *argument = arguments[index];
        bool keep = true;
        const char *value = NULL;

        if (strncmp(argument, "--provider=", 11) == 0) {
            value = argument + 11;
            options->provider_explicit = true;
            keep = false;
        } else if (strcmp(argument, "--provider") == 0) {
            if (++index >= *argc) return -1;
            value = arguments[index];
            options->provider_explicit = true;
            keep = false;
        }
        if (value) {
            if (!*value) return -1;
            options->provider = value;
            continue;
        }

#define KEEP_VALUE_OPTION(long_name, destination)                                      \
        if (strncmp(argument, long_name "=", sizeof(long_name)) == 0) {                \
            destination = argument + sizeof(long_name);                                \
        } else if (strcmp(argument, long_name) == 0) {                                 \
            if (index + 1 >= *argc) return -1;                                         \
            destination = arguments[index + 1];                                       \
            arguments[output++] = arguments[index++];                                 \
            arguments[output++] = arguments[index];                                   \
            continue;                                                                  \
        } else

        KEEP_VALUE_OPTION("--download-payload", options->download_payload) {
        KEEP_VALUE_OPTION("--download-framing", options->download_framing) {
        KEEP_VALUE_OPTION("--download-flush", options->download_flush) {
        if (strncmp(argument, "--download-chunk-bytes=", 23) == 0) {
            if (parse_size_value(argument + 23,
                                 &options->download_chunk_bytes) != 0) {
                return -1;
            }
        } else if (strcmp(argument, "--download-chunk-bytes") == 0) {
            if (index + 1 >= *argc ||
                parse_size_value(arguments[index + 1],
                                 &options->download_chunk_bytes) != 0) {
                return -1;
            }
            arguments[output++] = arguments[index++];
            arguments[output++] = arguments[index];
            continue;
        } else if (strncmp(argument, "--server=", 9) == 0) {
            options->server = argument + 9;
            server_explicit = true;
        } else if (strcmp(argument, "--server") == 0 ||
                   strcmp(argument, "-s") == 0) {
            if (index + 1 >= *argc) return -1;
            options->server = arguments[index + 1];
            server_explicit = true;
            arguments[output++] = arguments[index++];
            arguments[output++] = arguments[index];
            continue;
        } else if (strncmp(argument, "--token=", 8) == 0) {
            options->token = argument + 8;
        } else if (strcmp(argument, "--token") == 0) {
            if (index + 1 >= *argc) return -1;
            options->token = arguments[index + 1];
            arguments[output++] = arguments[index++];
            arguments[output++] = arguments[index];
            continue;
        } else if (strcmp(argument, "--json") == 0 ||
                   strcmp(argument, "-j") == 0) {
            options->json = true;
        } else if (strcmp(argument, "--quiet") == 0) {
            options->quiet = true;
        } else if (strcmp(argument, "--csv") == 0 ||
                   strcmp(argument, "-c") == 0) {
            options->csv = true;
        } else if (strcmp(argument, "--quick") == 0 ||
                   strcmp(argument, "-q") == 0) {
            options->quick = true;
        } else if (strcmp(argument, "--download-only") == 0 ||
                   strcmp(argument, "-d") == 0) {
            options->download_only = true;
        } else if (strcmp(argument, "--upload-only") == 0 ||
                   strcmp(argument, "-u") == 0) {
            options->upload_only = true;
        } else if (strncmp(argument, "--timeout=", 10) == 0) {
            /* Strict mode consumes the value if provider selection falls back. */
        } else if (strcmp(argument, "--timeout") == 0 ||
                   strcmp(argument, "-t") == 0) {
            if (index + 1 >= *argc) return -1;
            arguments[output++] = arguments[index++];
            arguments[output++] = arguments[index];
            continue;
        } else if (strcmp(argument, "--no-packet-loss") == 0 ||
                   strcmp(argument, "--skip-packet-loss") == 0) {
            options->skip_packet = true;
            if (strcmp(argument, "--skip-packet-loss") == 0) {
                argument = "--no-packet-loss";
            }
        } else if (strcmp(argument, "--insecure") == 0 ||
                   strcmp(argument, "-k") == 0) {
            options->insecure = true;
            keep = false;
        } else if (strncmp(argument, "--turn-credentials-url=", 23) == 0) {
            options->turn_credentials_url = argument + 23;
            keep = false;
        } else if (strcmp(argument, "--turn-credentials-url") == 0) {
            if (++index >= *argc) return -1;
            options->turn_credentials_url = arguments[index];
            keep = false;
        } else if (strncmp(argument, "--turn-url=", 11) == 0) {
            options->turn_url = argument + 11;
            keep = false;
        } else if (strcmp(argument, "--turn-url") == 0) {
            if (++index >= *argc) return -1;
            options->turn_url = arguments[index];
            keep = false;
        } else if (strncmp(argument, "--turn-username=", 16) == 0) {
            options->turn_username = argument + 16;
            keep = false;
        } else if (strcmp(argument, "--turn-username") == 0) {
            if (++index >= *argc) return -1;
            options->turn_username = arguments[index];
            keep = false;
        } else if (strncmp(argument, "--turn-credential=", 18) == 0) {
            options->turn_credential = argument + 18;
            keep = false;
        } else if (strcmp(argument, "--turn-credential") == 0 ||
                   strcmp(argument, "--turn-password") == 0) {
            if (++index >= *argc) return -1;
            options->turn_credential = arguments[index];
            keep = false;
        } else if (argument[0] != '-') {
            if (server_explicit || positional_server) return -1;
            options->server = argument;
            positional_server = true;
        }

        if (keep) {
            arguments[output++] = argument;
        }
        }
        }
        }
#undef KEEP_VALUE_OPTION
    }

    arguments[output] = NULL;
    *argc = output;

    if (!options->provider_explicit) {
        const char *provider = getenv("NETSPEED_PROVIDER");
        if (provider && *provider) options->provider = provider;
    }
    if (!options->token) options->token = getenv("NETSPEED_TOKEN");
    if (!options->turn_credentials_url) {
        options->turn_credentials_url = getenv("NETSPEED_TURN_CREDENTIALS_URL");
    }
    if (!options->turn_url) options->turn_url = getenv("NETSPEED_TURN_URL");
    if (!options->turn_username) {
        options->turn_username = getenv("NETSPEED_TURN_USERNAME");
    }
    if (!options->turn_credential) {
        options->turn_credential = getenv("NETSPEED_TURN_CREDENTIAL");
    }

    if (strcasecmp(options->download_payload, "auto") != 0 &&
        strcasecmp(options->download_payload, "random") != 0 &&
        strcasecmp(options->download_payload, "zero") != 0) {
        return -1;
    }
    if (strcasecmp(options->download_framing, "auto") != 0 &&
        strcasecmp(options->download_framing, "fixed") != 0 &&
        strcasecmp(options->download_framing, "chunked") != 0) {
        return -1;
    }
    if (strcasecmp(options->download_flush, "auto") != 0 &&
        strcasecmp(options->download_flush, "true") != 0 &&
        strcasecmp(options->download_flush, "false") != 0) {
        return -1;
    }
    if (options->download_only && options->upload_only) {
        return -1;
    }
    if ((options->json ? 1 : 0) + (options->csv ? 1 : 0) +
            (options->quiet ? 1 : 0) >
        1) {
        return -1;
    }
    if (!options->server ||
        (strncmp(options->server, "http://", 7) != 0 &&
         strncmp(options->server, "https://", 8) != 0)) {
        return -1;
    }
    return 0;
}

static int probe_provider(const cf_options *options, bool *netspeed,
                          bool *incompatible, bool *cloudflare)
{
    *netspeed = false;
    *incompatible = false;
    *cloudflare = false;

    cf_http_session session;
    cf_http_session_init(&session, options);
    if (!session.easy) {
        return -1;
    }
    buffer body = {0};
    cf_response response;
    int status = cf_http_request(&session, "GET", "/meta", NULL, 0, -1,
                                 &body, CF_MAX_RESPONSE_BODY, NULL, &response);
    if (status == CF_OK && has_netspeed_marker(body.data)) {
        *netspeed = strstr(body.data, "\"measurementProtocolVersion\":2") != NULL ||
                    strstr(body.data, "\"measurementApiVersion\":2") != NULL;
        *incompatible = !*netspeed;
        free(body.data);
        cf_http_session_cleanup(&session);
        return 0;
    }
    free(body.data);
    memset(&body, 0, sizeof(body));

    status = cf_http_request(&session, "GET", "/__down",
                             "?bytes=0&compat=1", 0, 0, &body, 1, NULL,
                             &response);
    if (status == CF_OK) {
        *cloudflare = host_is_cloudflare(options->server) ||
                      response.cloudflare_fingerprint;
    }
    free(body.data);
    cf_http_session_cleanup(&session);
    return status == CF_OK ? 0 : -1;
}

static int classify_payload(distributed_capture *capture, char *classification,
                            size_t classification_capacity, char *evidence,
                            size_t evidence_capacity)
{
    if (!capture || !capture_complete(capture)) {
        snprintf(evidence, evidence_capacity, "%s", "incomplete capture");
        return CF_PROTOCOL_ERROR;
    }
    size_t length = capture_compact(capture);
    if (!length) {
        snprintf(classification, classification_capacity, "%s", "empty");
        snprintf(evidence, evidence_capacity, "%s", "zero-length-body");
        return CF_OK;
    }
    bool all_zero = true;
    bool all_ascii_zero = true;
    uint64_t counts[256] = {0};
    for (size_t index = 0; index < length; index++) {
        unsigned char value = capture->data[index];
        counts[value]++;
        if (value != 0) all_zero = false;
        if (value != '0') all_ascii_zero = false;
    }
    if (all_zero) {
        snprintf(classification, classification_capacity, "%s", "zero");
        snprintf(evidence, evidence_capacity, "%s", "body-all-0x00");
        return CF_OK;
    }
    if (all_ascii_zero) {
        snprintf(classification, classification_capacity, "%s", "ascii-zero");
        snprintf(evidence, evidence_capacity, "%s", "body-all-0x30");
        return CF_OK;
    }

    double entropy = 0;
    for (size_t index = 0; index < 256; index++) {
        if (!counts[index]) continue;
        double probability = (double)counts[index] / (double)length;
        entropy -= probability * log2(probability);
    }
    uLongf compressed_capacity = compressBound((uLong)length);
    unsigned char *compressed = malloc((size_t)compressed_capacity);
    double ratio = 0;
    if (compressed &&
        compress2(compressed, &compressed_capacity, capture->data,
                  (uLong)length, Z_BEST_SPEED) == Z_OK) {
        ratio = (double)compressed_capacity / (double)length;
    }
    free(compressed);
    snprintf(evidence, evidence_capacity,
             "body-entropy=%.3f-bits-per-byte,zlib-ratio=%.3f", entropy,
             ratio);
    snprintf(classification, classification_capacity, "%s",
             length >= 4096 && entropy >= 7.5 && ratio >= 0.95 ? "random"
                                                               : "opaque");
    return CF_OK;
}

static int inspect_payload(cf_response *response, distributed_capture *capture,
                           char *classification, size_t classification_capacity,
                           char *evidence, size_t evidence_capacity,
                           char *error, size_t error_capacity)
{
    int status = classify_payload(capture, classification,
                                  classification_capacity, evidence,
                                  evidence_capacity);
    if (status != CF_OK) {
        snprintf(error, error_capacity, "%s", "download payload capture was incomplete");
        return status;
    }
    if (!response->payload[0]) {
        return CF_OK;
    }
    if (strcasecmp(response->payload, "random") != 0 &&
        strcasecmp(response->payload, "zero") != 0) {
        snprintf(error, error_capacity,
                 "unsupported X-Netspeed-Payload %s", response->payload);
        return CF_PROTOCOL_ERROR;
    }
    if (strcasecmp(response->payload, classification) != 0) {
        snprintf(error, error_capacity,
                 "download claimed payload %s but body classified as %s",
                 response->payload, classification);
        return CF_PROTOCOL_ERROR;
    }
    char body_evidence[192];
    snprintf(body_evidence, sizeof(body_evidence), "%s", evidence);
    snprintf(evidence, evidence_capacity, "X-Netspeed-Payload=%.31s; %.120s",
             response->payload, body_evidence);
    return CF_OK;
}

static int inspect_framing(const cf_response *response, uint64_t expected_bytes,
                           char *framing, size_t framing_capacity,
                           char *evidence, size_t evidence_capacity,
                           char *error, size_t error_capacity)
{
    bool chunked = header_has_directive(response->transfer_encoding, "chunked");
    if (response->framing[0]) {
        if (strcasecmp(response->framing, "fixed") == 0) {
            if (response->content_length != (int64_t)expected_bytes) {
                snprintf(error, error_capacity,
                         "download claimed fixed framing but Content-Length is %" PRId64,
                         response->content_length);
                return CF_PROTOCOL_ERROR;
            }
        } else if (strcasecmp(response->framing, "chunked") == 0) {
            if (response->content_length >= 0 ||
                (strncasecmp(response->http_protocol, "HTTP/1", 6) == 0 &&
                 !chunked)) {
                snprintf(error, error_capacity,
                         "%s", "download claimed chunked framing without HTTP chunked evidence");
                return CF_PROTOCOL_ERROR;
            }
        } else {
            snprintf(error, error_capacity,
                     "unsupported X-Netspeed-Framing %s", response->framing);
            return CF_PROTOCOL_ERROR;
        }
        snprintf(framing, framing_capacity, "%.15s", response->framing);
        snprintf(evidence, evidence_capacity,
                 "X-Netspeed-Framing=%s; protocol=%s", response->framing,
                 response->http_protocol);
        return CF_OK;
    }
    if (response->content_length >= 0) {
        if (response->content_length != (int64_t)expected_bytes) {
            snprintf(error, error_capacity,
                     "download Content-Length is %" PRId64 "; expected %" PRIu64,
                     response->content_length, expected_bytes);
            return CF_PROTOCOL_ERROR;
        }
        snprintf(framing, framing_capacity, "%s", "fixed");
        snprintf(evidence, evidence_capacity,
                 "Content-Length=%" PRId64 "; protocol=%s",
                 response->content_length, response->http_protocol);
        return CF_OK;
    }
    if (strncasecmp(response->http_protocol, "HTTP/1", 6) == 0 && chunked) {
        snprintf(framing, framing_capacity, "%s", "chunked");
        snprintf(evidence, evidence_capacity,
                 "Transfer-Encoding=chunked; protocol=%s",
                 response->http_protocol);
        return CF_OK;
    }
    snprintf(framing, framing_capacity, "%s", "streamed");
    snprintf(evidence, evidence_capacity, "no-Content-Length; protocol=%s",
             response->http_protocol);
    return CF_OK;
}

static bool content_encoding_is_identity(const char *value)
{
    if (!value || !*value) {
        return true;
    }
    const char *cursor = value;
    bool saw_token = false;
    while (*cursor) {
        while (*cursor == ',' || isspace((unsigned char)*cursor)) {
            cursor++;
        }
        const char *start = cursor;
        while (*cursor && *cursor != ',') {
            cursor++;
        }
        const char *end = cursor;
        while (end > start && isspace((unsigned char)end[-1])) {
            end--;
        }
        if (end == start || (size_t)(end - start) != strlen("identity") ||
            strncasecmp(start, "identity", strlen("identity")) != 0) {
            return false;
        }
        saw_token = true;
    }
    return saw_token;
}

static int verify_identity(const cf_response *response, char *error,
                           size_t error_capacity)
{
    if (!content_encoding_is_identity(response->content_encoding)) {
        snprintf(error, error_capacity,
                 "measurement response used unsupported Content-Encoding %s",
                 response->content_encoding);
        return CF_PROTOCOL_ERROR;
    }
    return CF_OK;
}

static int probe_transport(const cf_options *options, cf_transport *transport,
                           char *error, size_t error_capacity)
{
    memset(transport, 0, sizeof(*transport));
    snprintf(transport->capability_source,
             sizeof(transport->capability_source), "%s", "behavioral-probe");
    transport->provider_defaults_only = true;
    transport->query_discriminators_sent = false;
    snprintf(transport->download_path, sizeof(transport->download_path), "%s",
             "/__down");
    snprintf(transport->upload_path, sizeof(transport->upload_path), "%s",
             "/__up");
    snprintf(transport->latency_path, sizeof(transport->latency_path), "%s",
             "/__down");
    snprintf(transport->bytes_parameter, sizeof(transport->bytes_parameter), "%s",
             "bytes");
    snprintf(transport->upload_payload, sizeof(transport->upload_payload), "%s",
             "ascii-zero");
    snprintf(transport->requested_payload,
             sizeof(transport->requested_payload), "%s",
             options->download_payload);
    snprintf(transport->requested_framing,
             sizeof(transport->requested_framing), "%s",
             options->download_framing);
    transport->requested_chunk_bytes = options->download_chunk_bytes;
    snprintf(transport->requested_flush,
             sizeof(transport->requested_flush), "%s",
             options->download_flush);
    transport->transport_compression_disabled = true;
    snprintf(transport->request_accept_encoding,
             sizeof(transport->request_accept_encoding), "%s", "identity");
    snprintf(transport->request_cache_control,
             sizeof(transport->request_cache_control), "%s",
             "no-store, no-transform");
    snprintf(transport->request_pragma, sizeof(transport->request_pragma), "%s",
             "no-cache");
    snprintf(transport->upload_content_encoding,
             sizeof(transport->upload_content_encoding), "%s", "identity");

    char query[96];
    snprintf(query, sizeof(query), "?bytes=%u&id=%" PRId64,
             CF_TRANSPORT_PROBE_BYTES, (int64_t)(mono() * 1000000000.0));
    cf_http_session session;
    cf_http_session_init(&session, options);
    if (!session.easy) {
        snprintf(error, error_capacity, "%s", "failed to initialize HTTP transport probe");
        return CF_ERROR;
    }
    distributed_capture capture;
    capture_init(&capture, CF_TRANSPORT_PROBE_BYTES);
    cf_response response;
    int status = cf_http_request(&session, "GET", transport->download_path,
                                 query, 0, CF_TRANSPORT_PROBE_BYTES, NULL, 0,
                                 &capture, &response);
    if (status != CF_OK) {
        snprintf(error, error_capacity, "Cloudflare transport probe failed: %s",
                 session.error[0] ? session.error : "network error");
        cf_http_session_cleanup(&session);
        return status;
    }
    cf_http_session_cleanup(&session);
    if (response.status / 100 != 2) {
        snprintf(error, error_capacity,
                 "Cloudflare transport probe returned HTTP %ld", response.status);
        return CF_PROTOCOL_ERROR;
    }
    if (response.body_bytes != CF_TRANSPORT_PROBE_BYTES) {
        snprintf(error, error_capacity,
                 "Cloudflare transport probe returned %" PRIu64 " of %u bytes",
                 response.body_bytes, CF_TRANSPORT_PROBE_BYTES);
        return CF_PROTOCOL_ERROR;
    }
    status = verify_identity(&response, error, error_capacity);
    if (status != CF_OK) return status;
    status = inspect_payload(&response, &capture, transport->selected_payload,
                             sizeof(transport->selected_payload),
                             transport->payload_evidence,
                             sizeof(transport->payload_evidence), error,
                             error_capacity);
    if (status != CF_OK) return status;
    status = inspect_framing(&response, CF_TRANSPORT_PROBE_BYTES,
                             transport->selected_framing,
                             sizeof(transport->selected_framing),
                             transport->framing_evidence,
                             sizeof(transport->framing_evidence), error,
                             error_capacity);
    if (status != CF_OK) return status;

    if (response.invalid_chunk_bytes_header) {
        snprintf(error, error_capacity, "%s",
                 "invalid X-Netspeed-Chunk-Bytes value");
        return CF_PROTOCOL_ERROR;
    }
    if (response.chunk_bytes_header_present) {
        transport->chunk_bytes_present = true;
        transport->chunk_bytes = (size_t)response.chunk_bytes;
        snprintf(transport->chunk_evidence, sizeof(transport->chunk_evidence),
                 "X-Netspeed-Chunk-Bytes=%" PRId64, response.chunk_bytes);
    }
    if (response.invalid_flush_header) {
        snprintf(error, error_capacity, "%s", "invalid X-Netspeed-Flush value");
        return CF_PROTOCOL_ERROR;
    }
    if (response.flush_present) {
        transport->flush_present = true;
        transport->flush = response.flush;
        snprintf(transport->flush_evidence, sizeof(transport->flush_evidence),
                 "X-Netspeed-Flush=%s", response.flush ? "true" : "false");
    }

    snprintf(transport->response_content_encoding,
             sizeof(transport->response_content_encoding), "%s",
             response.content_encoding[0] ? response.content_encoding : "identity");
    transport->response_no_store =
        header_has_directive(response.cache_control, "no-store");
    transport->response_no_transform =
        header_has_directive(response.cache_control, "no-transform");
    transport->proxy_buffer_suppression_observed =
        strcasecmp(response.x_accel_buffering, "no") == 0;

    if (strcasecmp(options->download_payload, "auto") != 0 &&
        strcasecmp(options->download_payload,
                   transport->selected_payload) != 0) {
        snprintf(error, error_capacity,
                 "Cloudflare endpoint provider-default payload is %s (%s); --download-payload=%s cannot be honored without sending a Netspeed-specific query parameter",
                 transport->selected_payload, transport->payload_evidence,
                 options->download_payload);
        return CF_CONTROL_ERROR;
    }
    if (strcasecmp(options->download_framing, "auto") != 0 &&
        strcasecmp(options->download_framing,
                   transport->selected_framing) != 0) {
        snprintf(error, error_capacity,
                 "Cloudflare endpoint provider-default framing is %s (%s); --download-framing=%s cannot be honored without sending a Netspeed-specific query parameter",
                 transport->selected_framing, transport->framing_evidence,
                 options->download_framing);
        return CF_CONTROL_ERROR;
    }
    if (options->download_chunk_bytes > 0) {
        if (!transport->chunk_bytes_present) {
            snprintf(error, error_capacity,
                     "cannot verify --download-chunk-bytes=%zu: endpoint supplied no exact X-Netspeed-Chunk-Bytes evidence",
                     options->download_chunk_bytes);
            return CF_CONTROL_ERROR;
        }
        if (transport->chunk_bytes != options->download_chunk_bytes) {
            snprintf(error, error_capacity,
                     "Cloudflare endpoint provider-default chunk size is %zu, not requested %zu",
                     transport->chunk_bytes, options->download_chunk_bytes);
            return CF_CONTROL_ERROR;
        }
    }
    if (strcasecmp(options->download_flush, "auto") != 0) {
        if (!transport->flush_present) {
            snprintf(error, error_capacity,
                     "cannot verify --download-flush=%s: endpoint supplied no X-Netspeed-Flush evidence",
                     options->download_flush);
            return CF_CONTROL_ERROR;
        }
        bool requested = strcasecmp(options->download_flush, "true") == 0;
        if (requested != transport->flush) {
            snprintf(error, error_capacity,
                     "Cloudflare endpoint provider-default flush is %s, not requested %s",
                     transport->flush ? "true" : "false",
                     requested ? "true" : "false");
            return CF_CONTROL_ERROR;
        }
    }
    return CF_OK;
}

static int verify_anti_transform_drift(const cf_response *response,
                                       const cf_transport *transport,
                                       char *error, size_t error_capacity)
{
    if (transport->response_no_store &&
        !header_has_directive(response->cache_control, "no-store")) {
        snprintf(error, error_capacity, "%s",
                 "measurement response lost probed Cache-Control no-store");
        return CF_PROTOCOL_ERROR;
    }
    if (transport->response_no_transform &&
        !header_has_directive(response->cache_control, "no-transform")) {
        snprintf(error, error_capacity, "%s",
                 "measurement response lost probed Cache-Control no-transform");
        return CF_PROTOCOL_ERROR;
    }
    if (transport->proxy_buffer_suppression_observed &&
        strcasecmp(response->x_accel_buffering, "no") != 0) {
        snprintf(error, error_capacity, "%s",
                 "measurement response lost probed X-Accel-Buffering: no");
        return CF_PROTOCOL_ERROR;
    }
    return CF_OK;
}

static int verify_download(const cf_response *response,
                           distributed_capture *capture, uint64_t expected_bytes,
                           const cf_transport *transport, char *error,
                           size_t error_capacity)
{
    int status = verify_identity(response, error, error_capacity);
    if (status != CF_OK) return status;
    if (response->invalid_chunk_bytes_header) {
        snprintf(error, error_capacity, "%s",
                 "download returned invalid X-Netspeed-Chunk-Bytes");
        return CF_PROTOCOL_ERROR;
    }
    if (response->invalid_flush_header) {
        snprintf(error, error_capacity, "%s",
                 "download returned invalid X-Netspeed-Flush");
        return CF_PROTOCOL_ERROR;
    }
    if (response->status / 100 != 2) {
        snprintf(error, error_capacity, "download returned HTTP %ld",
                 response->status);
        return CF_PROTOCOL_ERROR;
    }
    if (response->body_bytes != expected_bytes) {
        snprintf(error, error_capacity,
                 "download returned %" PRIu64 " of %" PRIu64 " bytes",
                 response->body_bytes, expected_bytes);
        return CF_PROTOCOL_ERROR;
    }
    status = verify_anti_transform_drift(response, transport, error,
                                         error_capacity);
    if (status != CF_OK) return status;
    char payload[16];
    char payload_evidence[192];
    status = inspect_payload((cf_response *)response, capture, payload,
                             sizeof(payload), payload_evidence,
                             sizeof(payload_evidence), error, error_capacity);
    if (status != CF_OK) return status;
    if (strcasecmp(payload, transport->selected_payload) != 0) {
        snprintf(error, error_capacity,
                 "download payload drifted from %s to %s",
                 transport->selected_payload, payload);
        return CF_PROTOCOL_ERROR;
    }
    char framing[16];
    char framing_evidence[192];
    status = inspect_framing(response, expected_bytes, framing,
                             sizeof(framing), framing_evidence,
                             sizeof(framing_evidence), error, error_capacity);
    if (status != CF_OK) return status;
    if (strcasecmp(framing, transport->selected_framing) != 0) {
        snprintf(error, error_capacity,
                 "download framing drifted from %s to %s",
                 transport->selected_framing, framing);
        return CF_PROTOCOL_ERROR;
    }
    if (transport->chunk_bytes_present &&
        response->chunk_bytes != (int64_t)transport->chunk_bytes) {
        snprintf(error, error_capacity,
                 "download chunk-size evidence drifted from %zu",
                 transport->chunk_bytes);
        return CF_PROTOCOL_ERROR;
    }
    if (transport->flush_present &&
        (!response->flush_present || response->flush != transport->flush)) {
        snprintf(error, error_capacity,
                 "download flush evidence drifted from %s",
                 transport->flush ? "true" : "false");
        return CF_PROTOCOL_ERROR;
    }
    return CF_OK;
}

static int verify_latency(const cf_response *response,
                          const cf_transport *transport, char *error,
                          size_t error_capacity)
{
    int status = verify_identity(response, error, error_capacity);
    if (status != CF_OK) return status;
    if (response->status / 100 != 2 || response->body_bytes != 0 ||
        (response->content_length >= 0 && response->content_length != 0)) {
        snprintf(error, error_capacity,
                 "zero-byte latency request returned HTTP %ld and %" PRIu64 " bytes",
                 response->status, response->body_bytes);
        return CF_PROTOCOL_ERROR;
    }
    return verify_anti_transform_drift(response, transport, error,
                                       error_capacity);
}

static int transfer_once_session(cf_http_session *session,
                                 const cf_transport *transport, bool upload,
                                 size_t bytes, double *mbps, char *error,
                                 size_t error_capacity)
{
    char query[128];
    snprintf(query, sizeof(query), "?bytes=%zu&id=%" PRId64, bytes,
             (int64_t)(mono() * 1000000000.0));
    cf_response response;
    distributed_capture capture;
    distributed_capture *capture_pointer = NULL;
    if (!upload) {
        capture_init(&capture, bytes);
        capture_pointer = &capture;
    }
    int status = cf_http_request(session, upload ? "POST" : "GET",
                                 upload ? transport->upload_path
                                        : transport->download_path,
                                 query, upload ? bytes : 0,
                                 upload ? -1 : (int64_t)bytes, NULL,
                                 upload ? CF_MAX_RESPONSE_BODY : 0,
                                 capture_pointer, &response);
    if (status != CF_OK) {
        snprintf(error, error_capacity, "%s",
                 session->error[0] ? session->error : "HTTP request failed");
        return status;
    }
    if (upload) {
        status = verify_identity(&response, error, error_capacity);
        if (status != CF_OK) return status;
        if (response.status / 100 != 2) {
            snprintf(error, error_capacity, "upload returned HTTP %ld",
                     response.status);
            return CF_PROTOCOL_ERROR;
        }
    } else {
        status = verify_download(&response, &capture, bytes, transport,
                                 error, error_capacity);
        if (status != CF_OK) return status;
    }
    if (response.total_ms <= 0) {
        snprintf(error, error_capacity, "%s", "transfer duration was not positive");
        return CF_PROTOCOL_ERROR;
    }
    *mbps = (double)bytes * 8.0 / response.total_ms / 1000.0;
    return CF_OK;
}

static int transfer_once(const cf_options *options,
                         const cf_transport *transport, bool upload,
                         size_t bytes, double *mbps, char *error,
                         size_t error_capacity)
{
    cf_http_session session;
    cf_http_session_init(&session, options);
    if (!session.easy) {
        snprintf(error, error_capacity, "%s", "failed to initialize HTTP transfer");
        return CF_ERROR;
    }
    int status = transfer_once_session(&session, transport, upload, bytes,
                                       mbps, error, error_capacity);
    cf_http_session_cleanup(&session);
    return status;
}

static void *load_worker(void *opaque)
{
    load_state *state = opaque;
    cf_http_session session;
    cf_http_session_init(&session, state->options);
    if (!session.easy) {
        pthread_mutex_lock(&state->mutex);
        state->failed = true;
        snprintf(state->error, sizeof(state->error), "%s",
                 "failed to initialize throughput worker");
        pthread_mutex_unlock(&state->mutex);
        return NULL;
    }
    while (mono() < state->until) {
        double mbps = 0;
        char error[256] = {0};
        int status = transfer_once_session(&session, state->transport,
                                           state->upload, state->bytes,
                                           &mbps, error, sizeof(error));
        if (status != CF_OK) {
            pthread_mutex_lock(&state->mutex);
            state->failed = true;
            snprintf(state->error, sizeof(state->error), "%s", error);
            pthread_mutex_unlock(&state->mutex);
            break;
        }
        pthread_mutex_lock(&state->mutex);
        state->completed += state->bytes;
        if (dpush(&state->samples, mbps) != 0) {
            state->failed = true;
            snprintf(state->error, sizeof(state->error), "%s",
                     "throughput sample allocation failed");
        }
        bool failed = state->failed;
        pthread_mutex_unlock(&state->mutex);
        if (failed) break;
    }
    cf_http_session_cleanup(&session);
    return NULL;
}

static double prioritized_server_duration_ms(const char *header)
{
    if (!header || !*header) return 0;
    double cf_speed_total = 0;
    double app = 0;
    char copy[1024];
    snprintf(copy, sizeof(copy), "%s", header);
    char *save_entry = NULL;
    for (char *entry = strtok_r(copy, ",", &save_entry); entry;
         entry = strtok_r(NULL, ",", &save_entry)) {
        char *save_part = NULL;
        char *name = strtok_r(entry, ";", &save_part);
        if (!name) continue;
        name = trim_header_value(name);
        double duration = 0;
        for (char *part = strtok_r(NULL, ";", &save_part); part;
             part = strtok_r(NULL, ";", &save_part)) {
            part = trim_header_value(part);
            if (strncasecmp(part, "dur=", 4) == 0) {
                char *value = part + 4;
                if (*value == '"') value++;
                errno = 0;
                char *end = NULL;
                double parsed = strtod(value, &end);
                if (errno == 0 && end != value && parsed > CF_SERVER_TIMING_MIN_MS) {
                    duration = parsed;
                }
            }
        }
        if (duration <= 0) continue;
        if (strcasecmp(name, "cfReqDur") == 0 ||
            strcasecmp(name, "cfRequestDur") == 0 ||
            strcasecmp(name, "cfReqDuration") == 0 ||
            strcasecmp(name, "cfRequestDuration") == 0) {
            return duration;
        }
        if (strncasecmp(name, "cfSpeed", 7) == 0) {
            cf_speed_total += duration;
        } else if (strcasecmp(name, "app") == 0) {
            app = duration;
        }
    }
    if (cf_speed_total > 0) return cf_speed_total;
    return app;
}

static void latency_add_protocol(cf_latency_session *session,
                                 const char *protocol)
{
    if (!protocol || !*protocol) return;
    for (int index = 0; index < session->protocol_count; index++) {
        if (strcmp(session->protocols[index], protocol) == 0) return;
    }
    if (session->protocol_count < CF_MAX_PROTOCOLS) {
        snprintf(session->protocols[session->protocol_count],
                 sizeof(session->protocols[session->protocol_count]), "%s",
                 protocol);
        session->protocol_count++;
    }
}

static int latency_attempt(cf_latency_session *session, const char *during,
                           int sequence, int attempt, double *milliseconds,
                           bool *reused, bool *adjusted, char *error,
                           size_t error_capacity)
{
    char query[192];
    snprintf(query, sizeof(query),
             "?bytes=0&during=%s&id=%" PRId64 "&seq=%d&attempt=%d",
             during, (int64_t)(mono() * 1000000000.0), sequence, attempt);
    cf_response response;
    int status = cf_http_request(&session->http, "GET",
                                 session->transport->latency_path, query, 0, 0,
                                 NULL, 0, NULL, &response);
    if (status != CF_OK) {
        snprintf(error, error_capacity, "%s",
                 session->http.error[0] ? session->http.error
                                        : "latency request failed");
        return status;
    }
    status = verify_latency(&response, session->transport, error,
                            error_capacity);
    if (status != CF_OK) return status;
    if (response.request_to_first_byte_ms <= 0) {
        snprintf(error, error_capacity, "%s",
                 "latency request produced non-positive first-byte timing");
        return CF_PROTOCOL_ERROR;
    }
    *milliseconds = response.request_to_first_byte_ms;
    *adjusted = false;
    double server_duration =
        prioritized_server_duration_ms(response.server_timing);
    if (server_duration > 0 && server_duration < *milliseconds) {
        *milliseconds -= server_duration;
        *adjusted = true;
    }
    if (*milliseconds <= 0) {
        snprintf(error, error_capacity, "%s",
                 "latency was non-positive after Server-Timing adjustment");
        return CF_PROTOCOL_ERROR;
    }
    *reused = response.connection_reused;
    latency_add_protocol(session, response.http_protocol);
    return CF_OK;
}

static int latency_session_init(cf_latency_session *session,
                                const cf_options *options,
                                const cf_transport *transport)
{
    memset(session, 0, sizeof(*session));
    session->options = options;
    session->transport = transport;
    cf_http_session_init(&session->http, options);
    return session->http.easy ? CF_OK : CF_ERROR;
}

static void latency_session_cleanup(cf_latency_session *session)
{
    cf_http_session_cleanup(&session->http);
}

static int latency_prime(cf_latency_session *session, const char *during,
                         char *error, size_t error_capacity)
{
    double milliseconds = 0;
    bool reused = false;
    bool adjusted = false;
    session->warmup_requests++;
    return latency_attempt(session, during, -1, 0, &milliseconds, &reused,
                           &adjusted, error, error_capacity);
}

static int latency_probe(cf_latency_session *session, const char *during,
                         int sequence, double *milliseconds, char *error,
                         size_t error_capacity)
{
    for (int attempt = 0; attempt < CF_LATENCY_ATTEMPTS; attempt++) {
        bool reused = false;
        bool adjusted = false;
        int status = latency_attempt(session, during, sequence, attempt,
                                     milliseconds, &reused, &adjusted, error,
                                     error_capacity);
        if (status != CF_OK) return status;
        if (!reused) {
            session->discarded_cold_attempts++;
            session->warmup_requests++;
            continue;
        }
        if (adjusted) session->server_timing_adjusted_samples++;
        return CF_OK;
    }
    snprintf(error, error_capacity,
             "Cloudflare latency connection was not reused after %d attempts",
             CF_LATENCY_ATTEMPTS);
    return CF_PROTOCOL_ERROR;
}

static latency_result latency_summary(const doubles *samples,
                                      const cf_latency_session *session,
                                      const char *failure)
{
    latency_result result = {0};
    result.samples = *samples;
    result.warm_samples = (int)samples->n;
    result.connection_reused = samples->n > 0;
    if (session) {
        result.warmup_requests = session->warmup_requests;
        result.discarded_cold_attempts = session->discarded_cold_attempts;
        result.server_timing_adjusted_samples =
            session->server_timing_adjusted_samples;
        result.protocol_count = session->protocol_count;
        for (int index = 0; index < session->protocol_count; index++) {
            snprintf(result.protocols[index], sizeof(result.protocols[index]),
                     "%s", session->protocols[index]);
        }
    }
    if (samples->n < 3) {
        snprintf(result.error, sizeof(result.error), "%s",
                 failure && *failure ? failure
                                     : "insufficient warm latency probes");
        return result;
    }
    result.available = true;
    result.median = percentile(samples, 0.5);
    result.p90 = percentile(samples, 0.9);
    result.jitter = result.p90 - result.median;
    return result;
}

static latency_result idle_latency(const cf_options *options,
                                   const cf_transport *transport)
{
    doubles samples = {0};
    cf_latency_session session;
    char error[256] = {0};
    if (latency_session_init(&session, options, transport) != CF_OK) {
        return latency_summary(&samples, NULL,
                               "failed to initialize latency session");
    }
    int status = latency_prime(&session, "idle", error, sizeof(error));
    int count = options->quick ? 5 : 10;
    for (int index = 0; status == CF_OK && index < count; index++) {
        double value = 0;
        status = latency_probe(&session, "idle", index, &value, error,
                               sizeof(error));
        if (status == CF_OK && dpush(&samples, value) != 0) {
            status = CF_ERROR;
            snprintf(error, sizeof(error), "%s",
                     "latency sample allocation failed");
        }
    }
    latency_result result = latency_summary(&samples, &session,
                                            status == CF_OK ? NULL : error);
    latency_session_cleanup(&session);
    return result;
}

static speed_result direction(const cf_options *options,
                              const cf_transport *transport, bool upload,
                              latency_result *loaded_latency)
{
    speed_result result = {0};
    size_t warmup_bytes = upload ? 512U * 1024U : 1024U * 1024U;
    double estimate = 0;
    char error[256] = {0};
    if (transfer_once(options, transport, upload, warmup_bytes, &estimate,
                      error, sizeof(error)) != CF_OK) {
        snprintf(result.error, sizeof(result.error),
                 "warmup transfer failed: %s", error);
        return result;
    }

    int workers = options->quick ? 2 : 4;
    double seconds = options->quick ? 0.9 : 1.8;
    size_t chunk =
        (size_t)(estimate * 1000000.0 * seconds / 8.0 / (double)workers);
    if (chunk < 256U * 1024U) chunk = 256U * 1024U;
    if (chunk > 32U * 1024U * 1024U) chunk = 32U * 1024U * 1024U;

    const char *condition = upload ? "upload" : "download";
    cf_latency_session latency;
    doubles latency_values = {0};
    int latency_status = latency_session_init(&latency, options, transport);
    if (latency_status == CF_OK) {
        latency_status = latency_prime(&latency, condition, error,
                                       sizeof(error));
    }

    load_state state = {
        .options = options,
        .transport = transport,
        .upload = upload,
        .bytes = chunk,
        .until = mono() + seconds,
        .mutex = PTHREAD_MUTEX_INITIALIZER,
    };
    pthread_t threads[4];
    bool created[4] = {false};
    for (int index = 0; index < workers; index++) {
        if (pthread_create(&threads[index], NULL, load_worker, &state) == 0) {
            created[index] = true;
        } else {
            pthread_mutex_lock(&state.mutex);
            state.failed = true;
            snprintf(state.error, sizeof(state.error), "%s",
                     "failed to create throughput worker");
            pthread_mutex_unlock(&state.mutex);
        }
    }

    sleep_us(100000);
    int probes = options->quick ? 4 : 8;
    for (int index = 0; latency_status == CF_OK && index < probes &&
                        mono() < state.until;
         index++) {
        double value = 0;
        latency_status = latency_probe(&latency, condition, index, &value,
                                       error, sizeof(error));
        if (latency_status == CF_OK && dpush(&latency_values, value) != 0) {
            latency_status = CF_ERROR;
            snprintf(error, sizeof(error), "%s",
                     "loaded-latency sample allocation failed");
        }
        sleep_us(40000);
    }
    for (int index = 0; index < workers; index++) {
        if (created[index]) pthread_join(threads[index], NULL);
    }

    if (latency.http.easy) {
        *loaded_latency = latency_summary(
            &latency_values, &latency,
            latency_status == CF_OK ? NULL : error);
        latency_session_cleanup(&latency);
    } else {
        *loaded_latency = latency_summary(
            &latency_values, NULL, "failed to initialize loaded-latency session");
    }

    pthread_mutex_lock(&state.mutex);
    bool failed = state.failed;
    char worker_error[256];
    snprintf(worker_error, sizeof(worker_error), "%s", state.error);
    pthread_mutex_unlock(&state.mutex);
    pthread_mutex_destroy(&state.mutex);
    if (failed) {
        snprintf(result.error, sizeof(result.error),
                 "throughput window failed: %.220s", worker_error);
        free(state.samples.v);
        return result;
    }
    if (!state.samples.n || !state.completed) {
        snprintf(result.error, sizeof(result.error), "%s",
                 "no complete transfer samples");
        free(state.samples.v);
        return result;
    }
    result.available = true;
    result.mbps = percentile(&state.samples, 0.9);
    result.samples = state.samples;
    result.evidence = upload ? "client-observed-complete-body"
                             : "exact-response-byte-count";
    return result;
}
#ifdef NS_HAVE_LIBDATACHANNEL
typedef struct{pthread_mutex_t mu;pthread_cond_t cv;int sender_pc,receiver_pc,sender_dc,receiver_dc;bool sender_open,receiver_open,sender_gathered,receiver_gathered;bool got[1001];} loop_ctx;
static void RTC_API gather_cb(int pc,rtcGatheringState st,void*ptr){loop_ctx*c=ptr;if(st==RTC_GATHERING_COMPLETE){pthread_mutex_lock(&c->mu);if(pc==c->sender_pc)c->sender_gathered=true;if(pc==c->receiver_pc)c->receiver_gathered=true;pthread_cond_broadcast(&c->cv);pthread_mutex_unlock(&c->mu);}}
static void RTC_API open_cb(int id,void*ptr){loop_ctx*c=ptr;pthread_mutex_lock(&c->mu);if(id==c->sender_dc)c->sender_open=true;if(id==c->receiver_dc)c->receiver_open=true;pthread_cond_broadcast(&c->cv);pthread_mutex_unlock(&c->mu);}
static void RTC_API msg_cb(int id,const char*m,int size,void*ptr){(void)id;loop_ctx*c=ptr;char b[32];if(size<0)size=(int)strlen(m);if(size<=0||size>30)return;memcpy(b,m,(size_t)size);b[size]=0;char*e=NULL;long n=strtol(b,&e,10);if(!e||*e||n<1||n>1000)return;pthread_mutex_lock(&c->mu);c->got[n]=true;pthread_cond_broadcast(&c->cv);pthread_mutex_unlock(&c->mu);}
static void RTC_API dc_cb(int pc,int dc,void*ptr){(void)pc;loop_ctx*c=ptr;pthread_mutex_lock(&c->mu);c->receiver_dc=dc;pthread_mutex_unlock(&c->mu);rtcSetUserPointer(dc,c);rtcSetOpenCallback(dc,open_cb);rtcSetMessageCallback(dc,msg_cb);}
static char*get_sdp(int pc){int n=rtcGetLocalDescription(pc,NULL,0);if(n<=0)return NULL;char*s=malloc((size_t)n);if(!s)return NULL;if(rtcGetLocalDescription(pc,s,n)<0){free(s);return NULL;}return s;}
static int wait_gather(loop_ctx*c,int pc){double end=mono()+12;pthread_mutex_lock(&c->mu);while(mono()<end){bool done=(pc==c->sender_pc)?c->sender_gathered:c->receiver_gathered;if(done){pthread_mutex_unlock(&c->mu);return 0;}struct timespec ts;clock_gettime(CLOCK_REALTIME,&ts);ts.tv_nsec+=100000000;if(ts.tv_nsec>=1000000000){ts.tv_sec++;ts.tv_nsec-=1000000000;}pthread_cond_timedwait(&c->cv,&c->mu,&ts);}pthread_mutex_unlock(&c->mu);return-1;}
static char*pct_encode(const char*s){size_t n=strlen(s),cap=n*3+1;char*out=malloc(cap),*p=out;if(!out)return NULL;const char*hex="0123456789ABCDEF";for(size_t i=0;i<n;i++){unsigned char c=(unsigned char)s[i];if((c>='a'&&c<='z')||(c>='A'&&c<='Z')||(c>='0'&&c<='9')||strchr("-._~",c))*p++=(char)c;else{*p++='%';*p++=hex[c>>4];*p++=hex[c&15];}}*p=0;return out;}
static int extract_json_string(const char*j,const char*key,char*out,size_t cap){char pat[96];snprintf(pat,sizeof(pat),"\"%s\"",key);const char*p=strstr(j,pat);if(!p)return-1;p=strchr(p+strlen(pat),':');if(!p)return-1;p++;while(*p==' '||*p=='\t')p++;if(*p=='['){p=strchr(p,'\"');}if(*p!='\"')return-1;p++;size_t n=0;while(*p&&*p!='\"'&&n+1<cap){if(*p=='\\'&&p[1])p++;out[n++]=*p++;}out[n]=0;return n?0:-1;}
static int extract_turn_url(const char*j,char*out,size_t cap){const char*p=j;while((p=strstr(p,"\"turn:"))!=NULL){p++;const char*e=strchr(p,'\"');if(!e)return-1;size_t n=(size_t)(e-p);if(n+1<cap){memcpy(out,p,n);out[n]=0;if(!strstr(out,"transport=")||contains_ci(out,"transport=udp"))return 0;}p=e+1;}return-1;}
static packet_result loopback(const cf_options*o){packet_result r={0};char url[1024]={0},user[512]={0},pass[512]={0};if(o->turn_url&&o->turn_username&&o->turn_credential){snprintf(url,sizeof(url),"%s",o->turn_url);snprintf(user,sizeof(user),"%s",o->turn_username);snprintf(pass,sizeof(pass),"%s",o->turn_credential);}else{const char*cu=o->turn_credentials_url;char*owned=NULL;if(!cu){owned=join_url(o->server,"/api/turn/credentials",NULL);cu=owned;}buffer b={0};long st=0;if(!cu||http_get_buffer(o,cu,&b,&st,NULL)!=0||st/100!=2||extract_json_string(b.data,"username",user,sizeof(user))||extract_json_string(b.data,"credential",pass,sizeof(pass))||(extract_turn_url(b.data,url,sizeof(url))&&extract_json_string(b.data,"server",url,sizeof(url)))){snprintf(r.reason,sizeof(r.reason),"TURN credentials unavailable");free(owned);free(b.data);return r;}free(owned);free(b.data);}if(strncmp(url,"turn:",5)){if(strstr(url,"://")||strchr(url,'?')){snprintf(r.reason,sizeof(r.reason),"TURN loopback requires a turn: UDP URL");return r;}size_t n=strlen(url);if(n+6>sizeof(url)){snprintf(r.reason,sizeof(r.reason),"TURN URL is too long");return r;}memmove(url+5,url,n+1);memcpy(url,"turn:",5);}if(!strstr(url,"transport="))strncat(url,strchr(url,'?')?"&transport=udp":"?transport=udp",sizeof(url)-strlen(url)-1);char*eu=pct_encode(user),*ep=pct_encode(pass);if(!eu||!ep){free(eu);free(ep);snprintf(r.reason,sizeof(r.reason),"credential encoding failed");return r;}const char*host=url+5;char uri[2048];snprintf(uri,sizeof(uri),"turn:%s:%s@%s",eu,ep,host);free(eu);free(ep);const char*servers[]={uri};rtcConfiguration cfg;memset(&cfg,0,sizeof(cfg));cfg.iceServers=servers;cfg.iceServersCount=1;cfg.iceTransportPolicy=RTC_TRANSPORT_POLICY_RELAY;cfg.disableAutoNegotiation=true;cfg.maxMessageSize=65536;loop_ctx c;memset(&c,0,sizeof(c));pthread_mutex_init(&c.mu,NULL);pthread_cond_init(&c.cv,NULL);c.sender_pc=rtcCreatePeerConnection(&cfg);c.receiver_pc=rtcCreatePeerConnection(&cfg);if(c.sender_pc<0||c.receiver_pc<0)goto fail;rtcSetUserPointer(c.sender_pc,&c);rtcSetUserPointer(c.receiver_pc,&c);rtcSetGatheringStateChangeCallback(c.sender_pc,gather_cb);rtcSetGatheringStateChangeCallback(c.receiver_pc,gather_cb);rtcSetDataChannelCallback(c.receiver_pc,dc_cb);rtcDataChannelInit di;memset(&di,0,sizeof(di));di.reliability.unordered=true;di.reliability.unreliable=true;di.reliability.maxRetransmits=0;c.sender_dc=rtcCreateDataChannelEx(c.sender_pc,"channel",&di);if(c.sender_dc<0)goto fail;rtcSetUserPointer(c.sender_dc,&c);rtcSetOpenCallback(c.sender_dc,open_cb);rtcSetLocalDescription(c.sender_pc,"offer");if(wait_gather(&c,c.sender_pc))goto fail;char*offer=get_sdp(c.sender_pc);if(!offer)goto fail;if(rtcSetRemoteDescription(c.receiver_pc,offer,"offer")<0){free(offer);goto fail;}free(offer);rtcSetLocalDescription(c.receiver_pc,"answer");if(wait_gather(&c,c.receiver_pc))goto fail;char*answer=get_sdp(c.receiver_pc);if(!answer)goto fail;if(rtcSetRemoteDescription(c.sender_pc,answer,"answer")<0){free(answer);goto fail;}free(answer);double end=mono()+15;pthread_mutex_lock(&c.mu);while((!c.sender_open||!c.receiver_open)&&mono()<end){struct timespec ts;clock_gettime(CLOCK_REALTIME,&ts);ts.tv_nsec+=100000000;if(ts.tv_nsec>=1000000000){ts.tv_sec++;ts.tv_nsec-=1000000000;}pthread_cond_timedwait(&c.cv,&c.mu,&ts);}bool opened=c.sender_open&&c.receiver_open;pthread_mutex_unlock(&c.mu);if(!opened)goto fail;for(int i=1;i<=1000;i++){char m[16];snprintf(m,sizeof(m),"%d",i);if(rtcSendMessage(c.sender_dc,m,-1)<0)goto fail;if(i%10==0)sleep_us(10000);}end=mono()+3;while(mono()<end){int got=0;pthread_mutex_lock(&c.mu);for(int i=1;i<=1000;i++)if(c.got[i])got++;pthread_mutex_unlock(&c.mu);if(got==1000)break;sleep_us(25000);}r.sent=1000;pthread_mutex_lock(&c.mu);for(int i=1;i<=1000;i++)if(c.got[i])r.received++;pthread_mutex_unlock(&c.mu);r.lost=r.sent-r.received;r.loss=(double)r.lost*100/r.sent;r.available=true;goto done;
fail:snprintf(r.reason,sizeof(r.reason),"TURN loopback negotiation failed");
done:if(c.sender_dc>0)rtcDeleteDataChannel(c.sender_dc);if(c.receiver_dc>0)rtcDeleteDataChannel(c.receiver_dc);if(c.sender_pc>0)rtcDeletePeerConnection(c.sender_pc);if(c.receiver_pc>0)rtcDeletePeerConnection(c.receiver_pc);pthread_cond_destroy(&c.cv);pthread_mutex_destroy(&c.mu);return r;}
#else
static packet_result loopback(const cf_options*o){(void)o;packet_result r={0};snprintf(r.reason,sizeof(r.reason),"C client was built without libdatachannel");return r;}
#endif

static void json_string(const char *value)
{
    putchar('"');
    const unsigned char *cursor = (const unsigned char *)(value ? value : "");
    for (; *cursor; cursor++) {
        switch (*cursor) {
        case '"': fputs("\\\"", stdout); break;
        case '\\': fputs("\\\\", stdout); break;
        case '\b': fputs("\\b", stdout); break;
        case '\f': fputs("\\f", stdout); break;
        case '\n': fputs("\\n", stdout); break;
        case '\r': fputs("\\r", stdout); break;
        case '\t': fputs("\\t", stdout); break;
        default:
            if (*cursor < 0x20) {
                printf("\\u%04x", *cursor);
            } else {
                putchar(*cursor);
            }
        }
    }
    putchar('"');
}

static void json_doubles(const doubles *values)
{
    putchar('[');
    for (size_t index = 0; index < values->n; index++) {
        if (index) putchar(',');
        printf("%.6f", values->v[index]);
    }
    putchar(']');
}

static void print_latency_json(const latency_result *result)
{
    printf("{\"available\":%s,\"medianMs\":",
           result->available ? "true" : "false");
    if (result->available) printf("%.6f", result->median); else fputs("null", stdout);
    fputs(",\"p90Ms\":", stdout);
    if (result->available) printf("%.6f", result->p90); else fputs("null", stdout);
    fputs(",\"jitterMs\":", stdout);
    if (result->available) printf("%.6f", result->jitter); else fputs("null", stdout);
    fputs(",\"samplesMs\":", stdout);
    json_doubles(&result->samples);
    printf(",\"connectionReused\":%s,\"warmSamples\":%d,"
           "\"warmupRequests\":%d,\"discardedColdAttempts\":%d,"
           "\"serverTimingAdjustedSamples\":%d,"
           "\"probeTransport\":\"http\",\"probeMethod\":\"GET\","
           "\"probePath\":\"/__down\",\"httpProtocols\":[",
           result->connection_reused ? "true" : "false",
           result->warm_samples, result->warmup_requests,
           result->discarded_cold_attempts,
           result->server_timing_adjusted_samples);
    for (int index = 0; index < result->protocol_count; index++) {
        if (index) putchar(',');
        json_string(result->protocols[index]);
    }
    putchar(']');
    if (!result->available && result->error[0]) {
        fputs(",\"error\":", stdout);
        json_string(result->error);
    }
    putchar('}');
}

static void print_speed_json(const speed_result *result, const char *fallback_evidence)
{
    printf("{\"available\":%s,\"mbps\":",
           result->available ? "true" : "false");
    if (result->available) printf("%.6f", result->mbps); else fputs("null", stdout);
    fputs(",\"samplesMbps\":", stdout);
    json_doubles(&result->samples);
    fputs(",\"evidence\":", stdout);
    json_string(result->evidence ? result->evidence : fallback_evidence);
    if (!result->available && result->error[0]) {
        fputs(",\"error\":", stdout);
        json_string(result->error);
    }
    putchar('}');
}

static void print_transport_json(const cf_transport *transport)
{
    fputs("{\"capabilitySource\":", stdout);
    json_string(transport->capability_source);
    printf(",\"providerDefaultsOnly\":%s,\"queryDiscriminatorsSent\":%s,",
           transport->provider_defaults_only ? "true" : "false",
           transport->query_discriminators_sent ? "true" : "false");
    fputs("\"downloadPath\":", stdout); json_string(transport->download_path);
    fputs(",\"uploadPath\":", stdout); json_string(transport->upload_path);
    fputs(",\"latencyPath\":", stdout); json_string(transport->latency_path);
    fputs(",\"bytesParameter\":", stdout); json_string(transport->bytes_parameter);
    fputs(",\"uploadPayload\":", stdout); json_string(transport->upload_payload);
    fputs(",\"selection\":{\"downloadPayloadRequested\":", stdout);
    json_string(transport->requested_payload);
    fputs(",\"downloadPayload\":", stdout); json_string(transport->selected_payload);
    fputs(",\"downloadPayloadEvidence\":", stdout); json_string(transport->payload_evidence);
    fputs(",\"downloadFramingRequested\":", stdout); json_string(transport->requested_framing);
    fputs(",\"downloadFraming\":", stdout); json_string(transport->selected_framing);
    fputs(",\"downloadFramingEvidence\":", stdout); json_string(transport->framing_evidence);
    printf(",\"downloadChunkBytesRequested\":%zu,\"downloadChunkBytes\":",
           transport->requested_chunk_bytes);
    if (transport->chunk_bytes_present) printf("%zu", transport->chunk_bytes); else fputs("null", stdout);
    if (transport->chunk_evidence[0]) {
        fputs(",\"downloadChunkBytesEvidence\":", stdout);
        json_string(transport->chunk_evidence);
    }
    fputs(",\"downloadFlushRequested\":", stdout); json_string(transport->requested_flush);
    fputs(",\"downloadFlush\":", stdout);
    if (transport->flush_present) fputs(transport->flush ? "true" : "false", stdout); else fputs("null", stdout);
    if (transport->flush_evidence[0]) {
        fputs(",\"downloadFlushEvidence\":", stdout);
        json_string(transport->flush_evidence);
    }
    fputs("},\"antiTransform\":{", stdout);
    printf("\"transportCompressionDisabled\":%s,",
           transport->transport_compression_disabled ? "true" : "false");
    fputs("\"requestAcceptEncoding\":", stdout); json_string(transport->request_accept_encoding);
    fputs(",\"requestCacheControl\":", stdout); json_string(transport->request_cache_control);
    fputs(",\"requestPragma\":", stdout); json_string(transport->request_pragma);
    fputs(",\"uploadContentEncoding\":", stdout); json_string(transport->upload_content_encoding);
    fputs(",\"responseContentEncoding\":", stdout); json_string(transport->response_content_encoding);
    printf(",\"responseNoStore\":%s,\"responseNoTransform\":%s,"
           "\"proxyBufferSuppressionObserved\":%s}}",
           transport->response_no_store ? "true" : "false",
           transport->response_no_transform ? "true" : "false",
           transport->proxy_buffer_suppression_observed ? "true" : "false");
}

static const char *number_or_na(bool available, double value, char *buffer_value,
                                size_t capacity)
{
    if (!available) {
        snprintf(buffer_value, capacity, "%s", "N/A");
    } else {
        snprintf(buffer_value, capacity, "%.2f", value);
    }
    return buffer_value;
}

static void print_result(const cf_options *options, const cf_transport *transport,
                         latency_result latency, speed_result download,
                         speed_result upload, latency_result download_loaded,
                         latency_result upload_loaded, packet_result packet)
{
    char a[32], b[32], c[32], d[32], e[32], f[32];
    if (options->json) {
        fputs("{\"provider\":\"cloudflare\","
              "\"measurementContract\":\"cloudflare-http-v2\","
              "\"uploadEvidence\":\"client-observed-complete-body\","
              "\"packetTopology\":\"turn-loopback\",\"server\":", stdout);
        json_string(options->server);
        fputs(",\"latency\":", stdout); print_latency_json(&latency);
        fputs(",\"download\":", stdout); print_speed_json(&download, "exact-response-byte-count");
        fputs(",\"upload\":", stdout); print_speed_json(&upload, "client-observed-complete-body");
        fputs(",\"downloadLoadedLatency\":", stdout); print_latency_json(&download_loaded);
        fputs(",\"uploadLoadedLatency\":", stdout); print_latency_json(&upload_loaded);
        fputs(",\"packetLoss\":{", stdout);
        printf("\"available\":%s,\"transport\":\"webrtc-datachannel-turn-udp\","
               "\"topology\":\"turn-loopback\",\"protocol\":\"cloudflare-loopback-v1\","
               "\"sent\":%d,\"received\":%d,\"transactionLossPercent\":",
               packet.available ? "true" : "false", packet.sent, packet.received);
        if (packet.available) printf("%.6f", packet.loss); else fputs("null", stdout);
        fputs(",\"forwardLossPercent\":null,\"reverseAcknowledgementLossPercent\":null", stdout);
        if (!packet.available) {
            fputs(",\"reason\":", stdout); json_string(packet.reason);
        }
        fputs("},\"httpTransport\":", stdout);
        print_transport_json(transport);
        fputs("}\n", stdout);
        return;
    }
    if (options->csv) {
        fputs("provider,contract,server,latency_ms,download_mbps,upload_mbps,download_loaded_ms,upload_loaded_ms,packet_loss_percent,packet_topology\n", stdout);
        printf("cloudflare,cloudflare-http-v2,%s,%s,%s,%s,%s,%s,%s,turn-loopback\n",
               options->server,
               number_or_na(latency.available, latency.median, a, sizeof(a)),
               number_or_na(download.available, download.mbps, b, sizeof(b)),
               number_or_na(upload.available, upload.mbps, c, sizeof(c)),
               number_or_na(download_loaded.available, download_loaded.p90, d, sizeof(d)),
               number_or_na(upload_loaded.available, upload_loaded.p90, e, sizeof(e)),
               number_or_na(packet.available, packet.loss, f, sizeof(f)));
        return;
    }
    if (options->quiet) {
        printf("%s %s %s %s cloudflare\n",
               number_or_na(download.available, download.mbps, a, sizeof(a)),
               number_or_na(upload.available, upload.mbps, b, sizeof(b)),
               number_or_na(latency.available, latency.median, c, sizeof(c)),
               number_or_na(packet.available, packet.loss, d, sizeof(d)));
        return;
    }
    printf("Provider:             cloudflare\n"
           "Measurement contract: cloudflare-http-v2\n"
           "HTTP download mode:   %s / %s (%s)\n"
           "Latency connection:   reused=%s; warm=%d; discarded-cold=%d\n"
           "Packet topology:      turn-loopback\n"
           "Latency:              %s ms\n"
           "Download:             %s Mbps\n"
           "Upload:               %s Mbps (client-observed-complete-body)\n"
           "Loaded latency down:  %s ms p90\n"
           "Loaded latency up:    %s ms p90\n"
           "Packet loss:          %s %%\n",
           transport->selected_payload, transport->selected_framing,
           transport->capability_source,
           latency.connection_reused ? "true" : "false",
           latency.warm_samples, latency.discarded_cold_attempts,
           number_or_na(latency.available, latency.median, a, sizeof(a)),
           number_or_na(download.available, download.mbps, b, sizeof(b)),
           number_or_na(upload.available, upload.mbps, c, sizeof(c)),
           number_or_na(download_loaded.available, download_loaded.p90, d, sizeof(d)),
           number_or_na(upload_loaded.available, upload_loaded.p90, e, sizeof(e)),
           number_or_na(packet.available, packet.loss, f, sizeof(f)));
    if (!packet.available) {
        printf("Packet test:          unavailable (%s)\n", packet.reason);
    }
}

static void free_latency_result(latency_result *result)
{
    free(result->samples.v);
    memset(&result->samples, 0, sizeof(result->samples));
}

static void free_speed_result(speed_result *result)
{
    free(result->samples.v);
    memset(&result->samples, 0, sizeof(result->samples));
}

int ns_cloudflare_dispatch(int *argc, char ***argv)
{
    cf_options options;
    memset(&options, 0, sizeof(options));
    if (parse_provider(&options, argc, argv) != 0) {
        fprintf(stderr, "netspeed: invalid provider arguments\n");
        return 2;
    }
    for (int index = 1; index < *argc; index++) {
        if (strcmp((*argv)[index], "--help") == 0 ||
            strcmp((*argv)[index], "-h") == 0 ||
            strcmp((*argv)[index], "--version") == 0 ||
            strcmp((*argv)[index], "-version") == 0) {
            return -1;
        }
    }
    if (strcasecmp(options.provider, "netspeed") == 0) {
        setenv("NETSPEED_SELECTED_PROVIDER", "netspeed", 1);
        return -1;
    }
    if (strcasecmp(options.provider, "auto") != 0 &&
        strcasecmp(options.provider, "cloudflare") != 0) {
        fprintf(stderr, "netspeed: invalid --provider value %s\n",
                options.provider);
        return 2;
    }
    if (curl_global_init(CURL_GLOBAL_DEFAULT) != CURLE_OK) {
        fprintf(stderr, "netspeed: failed to initialize libcurl\n");
        return 1;
    }
    if (strcasecmp(options.provider, "auto") == 0) {
        bool netspeed = false;
        bool incompatible = false;
        bool cloudflare = false;
        if (probe_provider(&options, &netspeed, &incompatible, &cloudflare) != 0 ||
            netspeed || incompatible || !cloudflare) {
            curl_global_cleanup();
            setenv("NETSPEED_SELECTED_PROVIDER", "netspeed", 1);
            return -1;
        }
    }

    cf_transport transport;
    char transport_error[512] = {0};
    int transport_status = probe_transport(&options, &transport,
                                           transport_error,
                                           sizeof(transport_error));
    if (transport_status != CF_OK) {
        fprintf(stderr, "netspeed: %s\n", transport_error);
        curl_global_cleanup();
        return transport_status == CF_CONTROL_ERROR ? 2 : 1;
    }

    latency_result latency = idle_latency(&options, &transport);
    latency_result download_loaded = {0};
    latency_result upload_loaded = {0};
    speed_result download = {0};
    speed_result upload = {0};
    if (!options.upload_only) {
        download = direction(&options, &transport, false, &download_loaded);
    }
    if (!options.download_only) {
        upload = direction(&options, &transport, true, &upload_loaded);
    }

    packet_result packet;
    if (options.skip_packet) {
        memset(&packet, 0, sizeof(packet));
        snprintf(packet.reason, sizeof(packet.reason), "%s", "skipped by request");
    } else {
        packet = loopback(&options);
    }
    print_result(&options, &transport, latency, download, upload,
                 download_loaded, upload_loaded, packet);

    int status = !latency.available ||
                         (!options.upload_only && !download.available) ||
                         (!options.download_only && !upload.available)
                     ? 1
                     : 0;
    free_latency_result(&latency);
    free_latency_result(&download_loaded);
    free_latency_result(&upload_loaded);
    free_speed_result(&download);
    free_speed_result(&upload);
    curl_global_cleanup();
    return status;
}
