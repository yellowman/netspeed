/*
 * measurement_transport.c - Versioned HTTP measurement transport negotiation.
 */
#include "measurement_transport.h"

#include "timing.h"
#include "websocket_ping.h"

#include <ctype.h>
#include <inttypes.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <strings.h>

#define PREFERENCE_AUTO "auto"

static void set_message(char *buffer, size_t capacity, const char *format, ...)
{
    if (!buffer || capacity == 0) {
        return;
    }
    va_list arguments;
    va_start(arguments, format);
    vsnprintf(buffer, capacity, format, arguments);
    va_end(arguments);
}

static const char *preference_or_auto(const char *value)
{
    return value && *value ? value : PREFERENCE_AUTO;
}

static bool token_equal(const char *left, const char *right)
{
    return left && right && strcasecmp(left, right) == 0;
}

static bool has_cache_directive(const char *value, const char *target)
{
    if (!value || !target) {
        return false;
    }
    const char *cursor = value;
    while (*cursor) {
        while (*cursor == ',' || isspace((unsigned char)*cursor)) {
            cursor++;
        }
        const char *start = cursor;
        while (*cursor && *cursor != ',' && *cursor != '=') {
            cursor++;
        }
        const char *end = cursor;
        while (end > start && isspace((unsigned char)end[-1])) {
            end--;
        }
        size_t length = (size_t)(end - start);
        if (length == strlen(target) && strncasecmp(start, target, length) == 0) {
            return true;
        }
        while (*cursor && *cursor != ',') {
            cursor++;
        }
    }
    return false;
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

static int validate_endpoint_path(const char *name, const char *value, bool required,
                                  char *error, size_t error_capacity)
{
    if (!value || !*value) {
        if (required) {
            set_message(error, error_capacity, "%s is required", name);
            return ERR_PROTOCOL;
        }
        return ERR_OK;
    }
    size_t length = strlen(value);
    if (value[0] != '/' || (length > 1 && value[1] == '/') ||
        (length > 1 && value[length - 1] == '/')) {
        set_message(error, error_capacity, "unsafe %s %s", name, value);
        return ERR_PROTOCOL;
    }
    const char *segment = value + 1;
    for (const char *cursor = value; ; cursor++) {
        unsigned char ch = (unsigned char)*cursor;
        if (ch == '\0' || ch == '/') {
            size_t segment_length = (size_t)(cursor - segment);
            if (cursor != value && segment_length == 0) {
                set_message(error, error_capacity, "unclean %s %s", name, value);
                return ERR_PROTOCOL;
            }
            if ((segment_length == 1 && segment[0] == '.') ||
                (segment_length == 2 && segment[0] == '.' && segment[1] == '.')) {
                set_message(error, error_capacity, "unclean %s %s", name, value);
                return ERR_PROTOCOL;
            }
            if (ch == '\0') {
                break;
            }
            segment = cursor + 1;
            continue;
        }
        /* Capability paths are deliberately a conservative URI-path subset.
         * Reject encoded separators/traversal rather than trying to normalize
         * an untrusted route differently from libcurl or a reverse proxy. */
        if (ch <= 0x20 || ch == 0x7f || ch == '\\' || ch == '?' || ch == '#' || ch == '%') {
            set_message(error, error_capacity, "unsafe %s %s", name, value);
            return ERR_PROTOCOL;
        }
    }
    return ERR_OK;
}

static int validate_parameter_name(const char *name, const char *value,
                                   char *error, size_t error_capacity)
{
    if (!value || !*value) {
        set_message(error, error_capacity, "%s is required", name);
        return ERR_PROTOCOL;
    }
    for (size_t index = 0; value[index]; index++) {
        unsigned char ch = (unsigned char)value[index];
        bool valid = isalpha(ch) || isdigit(ch) || ch == '_' || ch == '-' || ch == '.';
        if (!valid || (index == 0 && isdigit(ch))) {
            set_message(error, error_capacity, "unsafe %s %s", name, value);
            return ERR_PROTOCOL;
        }
    }
    return ERR_OK;
}

static int validate_capabilities(const measurement_capabilities_t *capabilities,
                                 char *error, size_t error_capacity)
{
    if (!capabilities || !capabilities->present) {
        set_message(error, error_capacity, "measurement capabilities are missing");
        return ERR_PROTOCOL;
    }
    if (capabilities->version < NETSPEED_HTTP_TRANSPORT_VERSION) {
        set_message(error, error_capacity,
                    "server HTTP transport capability version %d is too old; need %d",
                    capabilities->version, NETSPEED_HTTP_TRANSPORT_VERSION);
        return ERR_PROTOCOL;
    }
    int status = validate_endpoint_path("downloadPath", capabilities->download_path, true,
                                        error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }
    status = validate_endpoint_path("uploadPath", capabilities->upload_path, true,
                                    error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }
    status = validate_endpoint_path("httpPingPath", capabilities->http_ping_path, false,
                                    error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }
    status = validate_endpoint_path("webSocketPingPath", capabilities->websocket_ping_path,
                                    false, error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }
    if (!capabilities->websocket_ping_path[0]) {
        if (capabilities->websocket_ping_protocol[0] ||
            capabilities->websocket_ping_payload_bytes != 0) {
            set_message(error, error_capacity,
                        "WebSocket ping metadata is advertised without webSocketPingPath");
            return ERR_PROTOCOL;
        }
    } else {
        if (strcmp(capabilities->websocket_ping_protocol,
                   NETSPEED_WEBSOCKET_PING_PROTOCOL) != 0) {
            set_message(error, error_capacity,
                        "unsupported WebSocket ping protocol %s",
                        capabilities->websocket_ping_protocol[0]
                            ? capabilities->websocket_ping_protocol
                            : "<missing>");
            return ERR_PROTOCOL;
        }
        if (capabilities->websocket_ping_payload_bytes !=
            NETSPEED_WEBSOCKET_PING_PAYLOAD_BYTES) {
            set_message(error, error_capacity,
                        "unsupported WebSocket ping payload size %d; need %d",
                        capabilities->websocket_ping_payload_bytes,
                        NETSPEED_WEBSOCKET_PING_PAYLOAD_BYTES);
            return ERR_PROTOCOL;
        }
    }

    const char *parameter_names[] = {
        capabilities->download_bytes_parameter,
        capabilities->download_payload_parameter,
        capabilities->download_framing_parameter,
        capabilities->download_chunk_bytes_parameter,
        capabilities->download_flush_parameter,
    };
    const char *parameter_fields[] = {
        "downloadBytesParameter",
        "downloadPayloadParameter",
        "downloadFramingParameter",
        "downloadChunkBytesParameter",
        "downloadFlushParameter",
    };
    for (size_t index = 0; index < sizeof(parameter_names) / sizeof(parameter_names[0]); index++) {
        status = validate_parameter_name(parameter_fields[index], parameter_names[index],
                                         error, error_capacity);
        if (status != ERR_OK) {
            return status;
        }
        for (size_t previous = 0; previous < index; previous++) {
            if (strcmp(parameter_names[index], parameter_names[previous]) == 0) {
                set_message(error, error_capacity,
                            "%s and %s must not use the same query parameter %s",
                            parameter_fields[previous], parameter_fields[index],
                            parameter_names[index]);
                return ERR_PROTOCOL;
            }
        }
    }
    status = validate_parameter_name("uploadBytesParameter",
                                     capabilities->upload_bytes_parameter,
                                     error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }

    if (!token_equal(capabilities->default_download_payload, "random") &&
        !token_equal(capabilities->default_download_payload, "zero")) {
        set_message(error, error_capacity, "invalid default download payload %s",
                    capabilities->default_download_payload);
        return ERR_PROTOCOL;
    }
    if ((token_equal(capabilities->default_download_payload, "random") &&
         !capabilities->download_payload_random) ||
        (token_equal(capabilities->default_download_payload, "zero") &&
         !capabilities->download_payload_zero)) {
        set_message(error, error_capacity,
                    "default download payload %s is not advertised as supported",
                    capabilities->default_download_payload);
        return ERR_PROTOCOL;
    }
    if (!capabilities->download_payload_random) {
        set_message(error, error_capacity,
                    "transport version %d must support pseudorandom downloads",
                    NETSPEED_HTTP_TRANSPORT_VERSION);
        return ERR_PROTOCOL;
    }

    if (!token_equal(capabilities->default_download_framing, "fixed") &&
        !token_equal(capabilities->default_download_framing, "chunked")) {
        set_message(error, error_capacity, "invalid default download framing %s",
                    capabilities->default_download_framing);
        return ERR_PROTOCOL;
    }
    if ((token_equal(capabilities->default_download_framing, "fixed") &&
         !capabilities->download_framing_fixed) ||
        (token_equal(capabilities->default_download_framing, "chunked") &&
         !capabilities->download_framing_chunked)) {
        set_message(error, error_capacity,
                    "default download framing %s is not advertised as supported",
                    capabilities->default_download_framing);
        return ERR_PROTOCOL;
    }
    if (!capabilities->download_framing_fixed) {
        set_message(error, error_capacity,
                    "transport version %d must support fixed framing",
                    NETSPEED_HTTP_TRANSPORT_VERSION);
        return ERR_PROTOCOL;
    }
    if (capabilities->minimum_chunk_bytes <= 0 ||
        capabilities->maximum_chunk_bytes < capabilities->minimum_chunk_bytes) {
        set_message(error, error_capacity, "invalid advertised download chunk range %d..%d",
                    capabilities->minimum_chunk_bytes, capabilities->maximum_chunk_bytes);
        return ERR_PROTOCOL;
    }
    if (capabilities->default_chunk_bytes < capabilities->minimum_chunk_bytes ||
        capabilities->default_chunk_bytes > capabilities->maximum_chunk_bytes) {
        set_message(error, error_capacity,
                    "default download chunk size %d is outside advertised range %d..%d",
                    capabilities->default_chunk_bytes, capabilities->minimum_chunk_bytes,
                    capabilities->maximum_chunk_bytes);
        return ERR_PROTOCOL;
    }
    if (!capabilities->upload_content_encoding_identity) {
        set_message(error, error_capacity,
                    "server does not advertise identity upload content encoding");
        return ERR_PROTOCOL;
    }
    if (!capabilities->no_transform ||
        !has_cache_directive(capabilities->response_cache_control, "no-store") ||
        !has_cache_directive(capabilities->response_cache_control, "no-transform")) {
        set_message(error, error_capacity,
                    "server transport capabilities do not guarantee no-store, no-transform responses");
        return ERR_PROTOCOL;
    }
    if (capabilities->http_ping_path[0] &&
        !capabilities->http_ping_get && !capabilities->http_ping_head) {
        set_message(error, error_capacity,
                    "httpPingPath is advertised without a supported GET or HEAD method");
        return ERR_PROTOCOL;
    }
    if (capabilities->proxy_buffer_suppression_header[0] &&
        strcasecmp(capabilities->proxy_buffer_suppression_header,
                   "X-Accel-Buffering: no") != 0) {
        set_message(error, error_capacity,
                    "unsupported proxy buffer suppression contract %s",
                    capabilities->proxy_buffer_suppression_header);
        return ERR_PROTOCOL;
    }
    return ERR_OK;
}

void measurement_selection_legacy(measurement_selection_t *selection)
{
    if (!selection) {
        return;
    }
    memset(selection, 0, sizeof(*selection));
    selection->legacy_fallback = true;
    snprintf(selection->download_path, sizeof(selection->download_path), "%s", "/__down");
    snprintf(selection->download_bytes_parameter,
             sizeof(selection->download_bytes_parameter), "%s", "bytes");
    snprintf(selection->download_payload, sizeof(selection->download_payload), "%s", "random");
    snprintf(selection->download_framing, sizeof(selection->download_framing), "%s", "fixed");
    selection->download_chunk_bytes = NETSPEED_DEFAULT_CHUNK_BYTES;
    selection->download_flush = false;
    snprintf(selection->upload_path, sizeof(selection->upload_path), "%s", "/__up");
    snprintf(selection->upload_content_encoding,
             sizeof(selection->upload_content_encoding), "%s", "identity");
    snprintf(selection->latency_path, sizeof(selection->latency_path), "%s", "/__down");
    snprintf(selection->latency_method, sizeof(selection->latency_method), "%s", "GET");
    selection->latency_uses_download_endpoint = true;
    snprintf(selection->preferred_latency_transport,
             sizeof(selection->preferred_latency_transport), "%s", "http");
    selection->http_fallback_available = true;
}

int measurement_negotiate(const measurement_capabilities_t *capabilities,
                          const config_t *config,
                          measurement_selection_t *selection,
                          char *error, size_t error_capacity)
{
    if (!config || !selection) {
        set_message(error, error_capacity, "invalid transport negotiation arguments");
        return ERR_ARGS;
    }
    const char *payload_preference = preference_or_auto(config->download_payload);
    const char *framing_preference = preference_or_auto(config->download_framing);
    const char *flush_preference = preference_or_auto(config->download_flush);
    if (!token_equal(payload_preference, PREFERENCE_AUTO) &&
        !token_equal(payload_preference, "random") &&
        !token_equal(payload_preference, "zero")) {
        set_message(error, error_capacity,
                    "download payload must be auto, random, or zero");
        return ERR_ARGS;
    }
    if (!token_equal(framing_preference, PREFERENCE_AUTO) &&
        !token_equal(framing_preference, "fixed") &&
        !token_equal(framing_preference, "chunked")) {
        set_message(error, error_capacity,
                    "download framing must be auto, fixed, or chunked");
        return ERR_ARGS;
    }
    if (!token_equal(flush_preference, PREFERENCE_AUTO) &&
        !token_equal(flush_preference, "true") &&
        !token_equal(flush_preference, "false")) {
        set_message(error, error_capacity,
                    "download flush must be auto, true, or false");
        return ERR_ARGS;
    }
    if (config->download_chunk_bytes < 0) {
        set_message(error, error_capacity, "download chunk bytes cannot be negative");
        return ERR_ARGS;
    }
    bool explicit_preferences = !token_equal(payload_preference, PREFERENCE_AUTO) ||
                                !token_equal(framing_preference, PREFERENCE_AUTO) ||
                                config->download_chunk_bytes != 0 ||
                                !token_equal(flush_preference, PREFERENCE_AUTO);
    if (!capabilities || !capabilities->present) {
        if (explicit_preferences) {
            set_message(error, error_capacity,
                        "server does not advertise measurementCapabilities; explicit HTTP transport controls require transport version %d",
                        NETSPEED_HTTP_TRANSPORT_VERSION);
            return ERR_ARGS;
        }
        measurement_selection_legacy(selection);
        return ERR_OK;
    }

    int status = validate_capabilities(capabilities, error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }
    memset(selection, 0, sizeof(*selection));
    selection->capability_version = capabilities->version;

    const char *payload = token_equal(payload_preference, PREFERENCE_AUTO)
                              ? capabilities->default_download_payload
                              : payload_preference;
    if ((token_equal(payload, "random") && !capabilities->download_payload_random) ||
        (token_equal(payload, "zero") && !capabilities->download_payload_zero)) {
        set_message(error, error_capacity, "server does not support download payload %s",
                    payload);
        return ERR_ARGS;
    }
    const char *framing = token_equal(framing_preference, PREFERENCE_AUTO)
                              ? capabilities->default_download_framing
                              : framing_preference;
    if ((token_equal(framing, "fixed") && !capabilities->download_framing_fixed) ||
        (token_equal(framing, "chunked") && !capabilities->download_framing_chunked)) {
        set_message(error, error_capacity, "server does not support download framing %s",
                    framing);
        return ERR_ARGS;
    }
    int chunk_bytes = config->download_chunk_bytes != 0
                          ? config->download_chunk_bytes
                          : capabilities->default_chunk_bytes;
    if (chunk_bytes < capabilities->minimum_chunk_bytes ||
        chunk_bytes > capabilities->maximum_chunk_bytes) {
        set_message(error, error_capacity,
                    "download chunk size %d is outside server range %d..%d",
                    chunk_bytes, capabilities->minimum_chunk_bytes,
                    capabilities->maximum_chunk_bytes);
        return ERR_ARGS;
    }
    bool flush = token_equal(framing, "chunked");
    if (!token_equal(flush_preference, PREFERENCE_AUTO)) {
        flush = token_equal(flush_preference, "true");
    }

#define COPY_FIELD(destination, source) \
    snprintf(selection->destination, sizeof(selection->destination), "%s", capabilities->source)
    COPY_FIELD(download_path, download_path);
    COPY_FIELD(download_bytes_parameter, download_bytes_parameter);
    COPY_FIELD(download_payload_parameter, download_payload_parameter);
    COPY_FIELD(download_framing_parameter, download_framing_parameter);
    COPY_FIELD(download_chunk_bytes_parameter, download_chunk_bytes_parameter);
    COPY_FIELD(download_flush_parameter, download_flush_parameter);
    COPY_FIELD(upload_path, upload_path);
    COPY_FIELD(upload_bytes_parameter, upload_bytes_parameter);
    COPY_FIELD(response_cache_control, response_cache_control);
    COPY_FIELD(proxy_buffer_suppression_header, proxy_buffer_suppression_header);
    COPY_FIELD(websocket_ping_path, websocket_ping_path);
    COPY_FIELD(websocket_ping_protocol, websocket_ping_protocol);
#undef COPY_FIELD
    snprintf(selection->download_payload, sizeof(selection->download_payload), "%s", payload);
    snprintf(selection->download_framing, sizeof(selection->download_framing), "%s", framing);
    selection->download_chunk_bytes = chunk_bytes;
    selection->download_flush = flush;
    snprintf(selection->upload_content_encoding,
             sizeof(selection->upload_content_encoding), "%s", "identity");
    selection->no_transform = capabilities->no_transform;
    selection->websocket_ping_payload_bytes =
        capabilities->websocket_ping_payload_bytes;
    snprintf(selection->preferred_latency_transport,
             sizeof(selection->preferred_latency_transport), "%s",
             capabilities->websocket_ping_path[0] ? "websocket" : "http");
    selection->http_fallback_available = true;

    if (capabilities->http_ping_path[0]) {
        snprintf(selection->latency_path, sizeof(selection->latency_path), "%s",
                 capabilities->http_ping_path);
        snprintf(selection->latency_method, sizeof(selection->latency_method), "%s",
                 capabilities->http_ping_get ? "GET" : "HEAD");
    } else {
        snprintf(selection->latency_path, sizeof(selection->latency_path), "%s",
                 capabilities->download_path);
        snprintf(selection->latency_method, sizeof(selection->latency_method), "%s", "GET");
        selection->latency_uses_download_endpoint = true;
    }
    selection->warm_connection_ping = capabilities->warm_connection_ping;
    return ERR_OK;
}

static int append_format(char *path, size_t capacity, size_t *length,
                         const char *format, ...)
{
    if (!path || !length || *length >= capacity) {
        return ERR_ARGS;
    }
    va_list arguments;
    va_start(arguments, format);
    int written = vsnprintf(path + *length, capacity - *length, format, arguments);
    va_end(arguments);
    if (written < 0 || (size_t)written >= capacity - *length) {
        return ERR_ARGS;
    }
    *length += (size_t)written;
    return ERR_OK;
}

static int append_query(char *path, size_t capacity, size_t *length,
                        bool *has_query, const char *key, const char *value)
{
    if (!key || !*key || !value) {
        return ERR_OK;
    }
    int status = append_format(path, capacity, length, "%c%s=%s",
                               *has_query ? '&' : '?', key, value);
    if (status == ERR_OK) {
        *has_query = true;
    }
    return status;
}

static bool key_is_transport_parameter(const measurement_selection_t *selection,
                                       const char *key)
{
    const char *keys[] = {
        selection->download_bytes_parameter,
        selection->download_payload_parameter,
        selection->download_framing_parameter,
        selection->download_chunk_bytes_parameter,
        selection->download_flush_parameter,
        selection->upload_bytes_parameter,
    };
    for (size_t index = 0; index < sizeof(keys) / sizeof(keys[0]); index++) {
        if (keys[index][0] && strcmp(keys[index], key) == 0) {
            return true;
        }
    }
    return false;
}

static int initialize_path(const char *base, char *path, size_t capacity,
                           size_t *length, bool *has_query)
{
    if (!base || !*base || !path || capacity == 0) {
        return ERR_ARGS;
    }
    int written = snprintf(path, capacity, "%s", base);
    if (written < 0 || (size_t)written >= capacity) {
        return ERR_ARGS;
    }
    *length = (size_t)written;
    *has_query = strchr(base, '?') != NULL;
    return ERR_OK;
}

static int append_common_labels(const measurement_selection_t *selection,
                                char *path, size_t capacity, size_t *length,
                                bool *has_query, const char *profile, int run,
                                const char *condition, int attempt)
{
    char number[96];
    if (!key_is_transport_parameter(selection, "measId")) {
        snprintf(number, sizeof(number), "%" PRId64 "-%s-%d-%d",
                 timing_now_ms(), profile && *profile ? profile : "measurement", run, attempt);
        if (append_query(path, capacity, length, has_query, "measId", number) != ERR_OK) {
            return ERR_ARGS;
        }
    }
    if (profile && *profile && !key_is_transport_parameter(selection, "profile") &&
        append_query(path, capacity, length, has_query, "profile", profile) != ERR_OK) {
        return ERR_ARGS;
    }
    if (!key_is_transport_parameter(selection, "run")) {
        snprintf(number, sizeof(number), "%d", run);
        if (append_query(path, capacity, length, has_query, "run", number) != ERR_OK) {
            return ERR_ARGS;
        }
    }
    if (condition && *condition && !key_is_transport_parameter(selection, "during") &&
        append_query(path, capacity, length, has_query, "during", condition) != ERR_OK) {
        return ERR_ARGS;
    }
    if (!key_is_transport_parameter(selection, "seq")) {
        snprintf(number, sizeof(number), "%d", run);
        if (append_query(path, capacity, length, has_query, "seq", number) != ERR_OK) {
            return ERR_ARGS;
        }
    }
    if (attempt >= 0 && !key_is_transport_parameter(selection, "attempt")) {
        snprintf(number, sizeof(number), "%d", attempt);
        if (append_query(path, capacity, length, has_query, "attempt", number) != ERR_OK) {
            return ERR_ARGS;
        }
    }
    return ERR_OK;
}

int measurement_build_download_path(http_session_t *session,
                                    const measurement_selection_t *selection,
                                    int64_t bytes, const char *profile, int run,
                                    const char *condition,
                                    char *path, size_t path_capacity)
{
    (void)session;
    if (!selection || bytes < 0) {
        return ERR_ARGS;
    }
    size_t length = 0;
    bool has_query = false;
    int status = initialize_path(selection->download_path, path, path_capacity,
                                 &length, &has_query);
    if (status != ERR_OK) {
        return status;
    }
    char value[64];
    snprintf(value, sizeof(value), "%" PRId64, bytes);
    if (append_query(path, path_capacity, &length, &has_query,
                     selection->download_bytes_parameter, value) != ERR_OK) {
        return ERR_ARGS;
    }
    if (!selection->legacy_fallback) {
        if (append_query(path, path_capacity, &length, &has_query,
                         selection->download_payload_parameter,
                         selection->download_payload) != ERR_OK ||
            append_query(path, path_capacity, &length, &has_query,
                         selection->download_framing_parameter,
                         selection->download_framing) != ERR_OK) {
            return ERR_ARGS;
        }
        snprintf(value, sizeof(value), "%d", selection->download_chunk_bytes);
        if (append_query(path, path_capacity, &length, &has_query,
                         selection->download_chunk_bytes_parameter, value) != ERR_OK) {
            return ERR_ARGS;
        }
        if (append_query(path, path_capacity, &length, &has_query,
                         selection->download_flush_parameter,
                         selection->download_flush ? "true" : "false") != ERR_OK) {
            return ERR_ARGS;
        }
    }
    return append_common_labels(selection, path, path_capacity, &length, &has_query,
                                profile, run, condition, -1);
}

int measurement_build_upload_path(http_session_t *session,
                                  const measurement_selection_t *selection,
                                  int64_t bytes, const char *profile, int run,
                                  char *path, size_t path_capacity)
{
    (void)session;
    if (!selection || bytes < 0) {
        return ERR_ARGS;
    }
    size_t length = 0;
    bool has_query = false;
    int status = initialize_path(selection->upload_path, path, path_capacity,
                                 &length, &has_query);
    if (status != ERR_OK) {
        return status;
    }
    if (selection->upload_bytes_parameter[0]) {
        char value[64];
        snprintf(value, sizeof(value), "%" PRId64, bytes);
        if (append_query(path, path_capacity, &length, &has_query,
                         selection->upload_bytes_parameter, value) != ERR_OK) {
            return ERR_ARGS;
        }
    }
    return append_common_labels(selection, path, path_capacity, &length, &has_query,
                                profile, run, NULL, -1);
}

int measurement_build_latency_path(http_session_t *session,
                                   const measurement_selection_t *selection,
                                   const char *condition, int sequence, int attempt,
                                   char *path, size_t path_capacity)
{
    if (!selection) {
        return ERR_ARGS;
    }
    if (selection->latency_uses_download_endpoint) {
        int status = measurement_build_download_path(session, selection, 0, "latency",
                                                     sequence, condition, path,
                                                     path_capacity);
        if (status != ERR_OK || attempt < 0) {
            return status;
        }
        size_t length = strlen(path);
        bool has_query = strchr(path, '?') != NULL;
        char value[32];
        snprintf(value, sizeof(value), "%d", attempt);
        return key_is_transport_parameter(selection, "attempt")
                   ? ERR_OK
                   : append_query(path, path_capacity, &length, &has_query,
                                  "attempt", value);
    }
    size_t length = 0;
    bool has_query = false;
    int status = initialize_path(selection->latency_path, path, path_capacity,
                                 &length, &has_query);
    if (status != ERR_OK) {
        return status;
    }
    return append_common_labels(selection, path, path_capacity, &length, &has_query,
                                "latency", sequence, condition, attempt);
}

static int verify_common(const measurement_selection_t *selection,
                         const http_response_t *response,
                         const char *expected_measurement,
                         char *error, size_t error_capacity)
{
    if (!selection || !response) {
        set_message(error, error_capacity, "invalid measurement response verifier arguments");
        return ERR_ARGS;
    }
    if (!content_encoding_is_identity(response->content_encoding)) {
        set_message(error, error_capacity,
                    "measurement response used unsupported Content-Encoding %s",
                    response->content_encoding);
        return ERR_PROTOCOL;
    }
    if (selection->legacy_fallback) {
        return ERR_OK;
    }
    if (!has_cache_directive(response->cache_control, "no-store") ||
        !has_cache_directive(response->cache_control, "no-transform")) {
        set_message(error, error_capacity,
                    "measurement response Cache-Control %s does not preserve no-store, no-transform",
                    response->cache_control[0] ? response->cache_control : "<missing>");
        return ERR_PROTOCOL;
    }
    if (!token_equal(response->measurement, expected_measurement)) {
        set_message(error, error_capacity,
                    "measurement response type %s; expected %s",
                    response->measurement[0] ? response->measurement : "<missing>",
                    expected_measurement);
        return ERR_PROTOCOL;
    }
    if (selection->proxy_buffer_suppression_header[0] &&
        !token_equal(response->x_accel_buffering, "no")) {
        set_message(error, error_capacity,
                    "measurement response did not suppress reverse-proxy buffering");
        return ERR_PROTOCOL;
    }
    return ERR_OK;
}

int measurement_verify_download(const measurement_selection_t *selection,
                                const http_response_t *response,
                                int64_t expected_bytes,
                                const char *expected_measurement,
                                char *error, size_t error_capacity)
{
    int status = verify_common(selection, response, expected_measurement,
                               error, error_capacity);
    if (status != ERR_OK || selection->legacy_fallback) {
        return status;
    }
    if (!token_equal(response->payload, selection->download_payload)) {
        set_message(error, error_capacity,
                    "download response payload %s; expected %s",
                    response->payload[0] ? response->payload : "<missing>",
                    selection->download_payload);
        return ERR_PROTOCOL;
    }
    if (!token_equal(response->framing, selection->download_framing)) {
        set_message(error, error_capacity,
                    "download response framing %s; expected %s",
                    response->framing[0] ? response->framing : "<missing>",
                    selection->download_framing);
        return ERR_PROTOCOL;
    }
    if (response->chunk_bytes != selection->download_chunk_bytes) {
        set_message(error, error_capacity,
                    "download response chunk size %" PRId64 "; expected %d",
                    response->chunk_bytes, selection->download_chunk_bytes);
        return ERR_PROTOCOL;
    }
    const char *expected_flush = selection->download_flush ? "true" : "false";
    if (!token_equal(response->flush, expected_flush)) {
        set_message(error, error_capacity,
                    "download response flush %s; expected %s",
                    response->flush[0] ? response->flush : "<missing>",
                    expected_flush);
        return ERR_PROTOCOL;
    }
    if (token_equal(selection->download_framing, "fixed")) {
        if (response->content_length != expected_bytes) {
            set_message(error, error_capacity,
                        "fixed download Content-Length %" PRId64 "; expected %" PRId64,
                        response->content_length, expected_bytes);
            return ERR_PROTOCOL;
        }
    } else if (token_equal(selection->download_framing, "chunked")) {
        if (response->content_length >= 0) {
            set_message(error, error_capacity,
                        "streamed download unexpectedly supplied Content-Length %" PRId64,
                        response->content_length);
            return ERR_PROTOCOL;
        }
        if (strncasecmp(response->http_protocol, "HTTP/1", 6) == 0 &&
            !has_cache_directive(response->transfer_encoding, "chunked")) {
            set_message(error, error_capacity,
                        "HTTP/1.x streamed download did not use chunked transfer coding");
            return ERR_PROTOCOL;
        }
    } else {
        set_message(error, error_capacity, "unsupported negotiated download framing %s",
                    selection->download_framing);
        return ERR_PROTOCOL;
    }
    return ERR_OK;
}

int measurement_verify_upload(const measurement_selection_t *selection,
                              const http_response_t *response,
                              int64_t expected_bytes,
                              char *error, size_t error_capacity)
{
    int status = verify_common(selection, response, "upload", error, error_capacity);
    if (status != ERR_OK || selection->legacy_fallback) {
        return status;
    }
    if (!token_equal(response->payload, "discarded") ||
        !token_equal(response->framing, "fixed") ||
        !token_equal(response->upload_content_encoding, "identity")) {
        set_message(error, error_capacity,
                    "upload response discriminator mismatch payload=%s framing=%s encoding=%s",
                    response->payload[0] ? response->payload : "<missing>",
                    response->framing[0] ? response->framing : "<missing>",
                    response->upload_content_encoding[0]
                        ? response->upload_content_encoding : "<missing>");
        return ERR_PROTOCOL;
    }
    if (selection->upload_bytes_parameter[0] &&
        response->expected_upload_bytes != expected_bytes) {
        set_message(error, error_capacity,
                    "upload response expected-byte count %" PRId64 "; expected %" PRId64,
                    response->expected_upload_bytes, expected_bytes);
        return ERR_PROTOCOL;
    }
    if (response->accepted_upload_bytes != expected_bytes) {
        set_message(error, error_capacity,
                    "upload response accepted-byte count %" PRId64 "; expected %" PRId64,
                    response->accepted_upload_bytes, expected_bytes);
        return ERR_PROTOCOL;
    }
    if (response->upload_duration_ns <= 0) {
        set_message(error, error_capacity,
                    "upload response duration %" PRId64 " is not positive",
                    response->upload_duration_ns);
        return ERR_PROTOCOL;
    }
    return ERR_OK;
}

int measurement_verify_latency(const measurement_selection_t *selection,
                               const http_response_t *response,
                               char *error, size_t error_capacity)
{
    int status = verify_common(selection, response, "latency", error, error_capacity);
    if (status != ERR_OK) {
        return status;
    }
    if (response->content_length >= 0 && response->content_length != 0) {
        set_message(error, error_capacity,
                    "latency response Content-Length %" PRId64 "; expected 0",
                    response->content_length);
        return ERR_PROTOCOL;
    }
    return ERR_OK;
}
