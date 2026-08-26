/*
 * http.c - Bounded, persistent libcurl transport for the C client.
 */
#include "http.h"
#include "timing.h"

#include <ctype.h>
#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#define USER_AGENT "netspeed-c/" NETSPEED_VERSION
#define RESPONSE_SLACK 1

typedef enum {
    BODY_BUFFER,
    BODY_DISCARD
} body_mode_t;

typedef struct {
    http_response_t *response;
    body_mode_t mode;
    size_t max_body;
    int64_t expected_bytes;
    bool first_body;
    struct timespec first_body_at;
    struct timespec last_body_at;
    const http_activity_t *activity;
    bool activity_active;
    bool overflow;
} write_state_t;

typedef struct {
    int64_t remaining;
    int64_t transferred;
    struct timespec first_read_at;
    struct timespec last_read_at;
    bool first_read;
    const http_activity_t *activity;
    bool activity_active;
} read_state_t;

typedef struct {
    http_response_t *response;
    read_state_t *upload;
} header_state_t;

static void response_init(http_response_t *response)
{
    memset(response, 0, sizeof(*response));
    response->content_length = -1;
    snprintf(response->timing_source, sizeof(response->timing_source), "%s", "libcurl");
}

static void activity_begin(const http_activity_t *activity, bool *active)
{
    if (!activity || !activity->begin || *active) {
        return;
    }
    activity->begin(activity->opaque);
    *active = true;
}

static void activity_end(const http_activity_t *activity, bool *active)
{
    if (!activity || !activity->end || !*active) {
        return;
    }
    activity->end(activity->opaque);
    *active = false;
}

static int append_body(http_response_t *response, const void *data, size_t len,
                       size_t max_body)
{
    if (len > max_body || response->body_len > max_body - len) {
        return -1;
    }
    size_t needed = response->body_len + len + RESPONSE_SLACK;
    if (needed > response->body_capacity) {
        size_t capacity = response->body_capacity ? response->body_capacity : 4096;
        while (capacity < needed) {
            if (capacity > max_body / 2) {
                capacity = max_body + RESPONSE_SLACK;
                break;
            }
            capacity *= 2;
        }
        char *next = realloc(response->body, capacity);
        if (!next) {
            return -1;
        }
        response->body = next;
        response->body_capacity = capacity;
    }
    memcpy(response->body + response->body_len, data, len);
    response->body_len += len;
    response->body[response->body_len] = '\0';
    return 0;
}

static size_t write_callback(char *ptr, size_t size, size_t nmemb, void *opaque)
{
    write_state_t *state = opaque;
    if (size != 0 && nmemb > SIZE_MAX / size) {
        state->overflow = true;
        return 0;
    }
    size_t len = size * nmemb;
    if (len == 0) {
        return 0;
    }

    struct timespec now;
    timing_now(&now);
    if (!state->first_body) {
        state->first_body = true;
        state->first_body_at = now;
        activity_begin(state->activity, &state->activity_active);
    }
    state->last_body_at = now;

    if (state->response->transferred_bytes > INT64_MAX - (int64_t)len) {
        state->overflow = true;
        return 0;
    }
    state->response->transferred_bytes += (int64_t)len;

    if (state->mode == BODY_BUFFER &&
        append_body(state->response, ptr, len, state->max_body) != 0) {
        state->overflow = true;
        return 0;
    }
    if (state->mode == BODY_DISCARD && state->expected_bytes >= 0 &&
        state->response->transferred_bytes > state->expected_bytes) {
        state->overflow = true;
        return 0;
    }
    return len;
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

static size_t header_callback(char *buffer, size_t size, size_t nitems, void *opaque)
{
    header_state_t *state = opaque;
    http_response_t *response = state->response;
    if (size != 0 && nitems > SIZE_MAX / size) {
        return 0;
    }
    size_t len = size * nitems;
    if (len == 0 || len >= 4096) {
        return len;
    }
    char line[4096];
    memcpy(line, buffer, len);
    line[len] = '\0';

    /* The first final response status line proves the server has consumed the
     * request body. Keep upload load active until this point rather than ending
     * it when libcurl merely copies the last generator chunk into its local
     * send buffer. */
    if (state->upload && strncmp(line, "HTTP/", 5) == 0) {
        char *status_start = strchr(line, ' ');
        long status = status_start ? strtol(status_start + 1, NULL, 10) : 0;
        if (status >= 200) {
            activity_end(state->upload->activity, &state->upload->activity_active);
        }
    }

    char *colon = strchr(line, ':');
    if (!colon) {
        return len;
    }
    *colon = '\0';
    char *value = trim_header_value(colon + 1);
    if (strcasecmp(line, "Content-Length") == 0) {
        errno = 0;
        char *end = NULL;
        long long parsed = strtoll(value, &end, 10);
        if (errno == 0 && end && *end == '\0' && parsed >= 0) {
            response->content_length = (int64_t)parsed;
        }
    } else if (strcasecmp(line, "Content-Type") == 0) {
        snprintf(response->content_type, sizeof(response->content_type), "%s", value);
    } else if (strcasecmp(line, "Cache-Control") == 0) {
        snprintf(response->cache_control, sizeof(response->cache_control), "%s", value);
    }
    return len;
}

static size_t read_callback(char *buffer, size_t size, size_t nitems, void *opaque)
{
    read_state_t *state = opaque;
    if (size != 0 && nitems > SIZE_MAX / size) {
        return CURL_READFUNC_ABORT;
    }
    size_t capacity = size * nitems;
    if (capacity == 0 || state->remaining <= 0) {
        return 0;
    }
    size_t len = capacity;
    if ((int64_t)len > state->remaining) {
        len = (size_t)state->remaining;
    }
    memset(buffer, 0, len);
    struct timespec now;
    timing_now(&now);
    if (!state->first_read) {
        state->first_read = true;
        state->first_read_at = now;
        activity_begin(state->activity, &state->activity_active);
    }
    state->last_read_at = now;
    state->remaining -= (int64_t)len;
    state->transferred += (int64_t)len;
    return len;
}

static int progress_callback(void *opaque, curl_off_t dltotal, curl_off_t dlnow,
                             curl_off_t ultotal, curl_off_t ulnow)
{
    (void)dltotal;
    (void)dlnow;
    (void)ultotal;
    (void)ulnow;
    const http_session_t *session = opaque;
    if (!session) {
        return 0;
    }
    if (session->request_cancel && atomic_load(session->request_cancel)) {
        return 1;
    }
    const http_client_t *client = session->client;
    return client && client->aborted && *client->aborted ? 1 : 0;
}

static int join_url(const char *base, const char *path, char *out, size_t out_len)
{
    size_t base_len = strlen(base);
    bool base_slash = base_len > 0 && base[base_len - 1] == '/';
    bool path_slash = path[0] == '/';
    int written;
    if (base_slash && path_slash) {
        written = snprintf(out, out_len, "%.*s%s", (int)(base_len - 1), base, path);
    } else if (!base_slash && !path_slash) {
        written = snprintf(out, out_len, "%s/%s", base, path);
    } else {
        written = snprintf(out, out_len, "%s%s", base, path);
    }
    return written >= 0 && (size_t)written < out_len ? 0 : -1;
}

static struct curl_slist *request_headers(const http_client_t *client,
                                          const char *content_type)
{
    struct curl_slist *headers = NULL;
    headers = curl_slist_append(headers, "Accept: */*");
    headers = curl_slist_append(headers, "Accept-Encoding: identity");
    headers = curl_slist_append(headers, "Cache-Control: no-store, no-transform");
    headers = curl_slist_append(headers, "Pragma: no-cache");
    headers = curl_slist_append(headers, "Expect:");
    if (content_type) {
        char line[192];
        snprintf(line, sizeof(line), "Content-Type: %s", content_type);
        headers = curl_slist_append(headers, line);
    }
    if (client->access_token[0]) {
        size_t needed = strlen(client->access_token) + 32;
        char *line = malloc(needed);
        if (!line) {
            curl_slist_free_all(headers);
            return NULL;
        }
        snprintf(line, needed, "Authorization: Bearer %s", client->access_token);
        headers = curl_slist_append(headers, line);
        free(line);
    }
    return headers;
}

static void configure_common(http_session_t *session, const char *url,
                             write_state_t *write_state,
                             header_state_t *header_state,
                             struct curl_slist *headers)
{
    CURL *easy = session->easy;
    curl_easy_reset(easy);
    memset(session->error, 0, sizeof(session->error));
    curl_easy_setopt(easy, CURLOPT_ERRORBUFFER, session->error);
    curl_easy_setopt(easy, CURLOPT_URL, url);
    curl_easy_setopt(easy, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(easy, CURLOPT_USERAGENT, USER_AGENT);
    curl_easy_setopt(easy, CURLOPT_HTTP_VERSION, CURL_HTTP_VERSION_1_1);
    curl_easy_setopt(easy, CURLOPT_FOLLOWLOCATION, 0L);
    curl_easy_setopt(easy, CURLOPT_NOSIGNAL, 1L);
    curl_easy_setopt(easy, CURLOPT_TCP_NODELAY, 1L);
    curl_easy_setopt(easy, CURLOPT_TCP_KEEPALIVE, 1L);
    curl_easy_setopt(easy, CURLOPT_CONNECTTIMEOUT_MS, 30000L);
    curl_easy_setopt(easy, CURLOPT_TIMEOUT_MS, session->client->request_timeout_ms);
    curl_easy_setopt(easy, CURLOPT_ACCEPT_ENCODING, "identity");
    curl_easy_setopt(easy, CURLOPT_WRITEFUNCTION, write_callback);
    curl_easy_setopt(easy, CURLOPT_WRITEDATA, write_state);
    curl_easy_setopt(easy, CURLOPT_HEADERFUNCTION, header_callback);
    curl_easy_setopt(easy, CURLOPT_HEADERDATA, header_state);
    curl_easy_setopt(easy, CURLOPT_XFERINFOFUNCTION, progress_callback);
    curl_easy_setopt(easy, CURLOPT_XFERINFODATA, session);
    curl_easy_setopt(easy, CURLOPT_NOPROGRESS, 0L);
#ifdef CURLOPT_BUFFERSIZE
    curl_easy_setopt(easy, CURLOPT_BUFFERSIZE, 4L * 1024L * 1024L);
#endif
#ifdef CURLOPT_UPLOAD_BUFFERSIZE
    curl_easy_setopt(easy, CURLOPT_UPLOAD_BUFFERSIZE, 4L * 1024L * 1024L);
#endif
}

static int finish_request(http_session_t *session, http_response_t *response,
                          write_state_t *write_state, CURLcode code)
{
    activity_end(write_state->activity, &write_state->activity_active);
    if (write_state->first_body) {
        response->body_duration_ms = timing_diff_ms(&write_state->first_body_at,
                                                    &write_state->last_body_at);
    }
    if (code != CURLE_OK) {
        if (session->error[0] == '\0') {
            snprintf(session->error, sizeof(session->error), "%s", curl_easy_strerror(code));
        }
        return code == CURLE_OPERATION_TIMEDOUT ? ERR_TIMEOUT : ERR_NETWORK;
    }
    if (write_state->overflow) {
        snprintf(session->error, sizeof(session->error), "%s", "response exceeded the verified size limit");
        return ERR_PROTOCOL;
    }

    long status = 0;
    curl_easy_getinfo(session->easy, CURLINFO_RESPONSE_CODE, &status);
    response->status_code = (int)status;
    if (response->content_type[0] == '\0') {
        char *content_type = NULL;
        if (curl_easy_getinfo(session->easy, CURLINFO_CONTENT_TYPE, &content_type) == CURLE_OK && content_type) {
            snprintf(response->content_type, sizeof(response->content_type), "%s", content_type);
        }
    }
    if (response->content_length < 0) {
        curl_off_t content_length = -1;
        if (curl_easy_getinfo(session->easy, CURLINFO_CONTENT_LENGTH_DOWNLOAD_T, &content_length) == CURLE_OK &&
            content_length >= 0) {
            response->content_length = (int64_t)content_length;
        }
    }
    double pretransfer = 0;
    double starttransfer = 0;
    double total = 0;
    curl_easy_getinfo(session->easy, CURLINFO_PRETRANSFER_TIME, &pretransfer);
    curl_easy_getinfo(session->easy, CURLINFO_STARTTRANSFER_TIME, &starttransfer);
    curl_easy_getinfo(session->easy, CURLINFO_TOTAL_TIME, &total);
    response->request_to_first_byte_ms = (starttransfer - pretransfer) * 1000.0;
    if (response->body_duration_ms <= 0 && total >= starttransfer) {
        response->body_duration_ms = (total - starttransfer) * 1000.0;
    }
    return ERR_OK;
}

static int perform(http_session_t *session, const char *path, const char *method,
                   const char *content_type, body_mode_t body_mode, size_t max_body,
                   int64_t expected_download, read_state_t *read_state,
                   const http_activity_t *activity, http_response_t *response)
{
    response_init(response);
    if (!session || !session->easy || !session->client) {
        return ERR_ARGS;
    }
    char url[MAX_URL_LEN * 2];
    if (join_url(session->client->base_url, path, url, sizeof(url)) != 0) {
        snprintf(session->error, sizeof(session->error), "%s", "request URL is too long");
        return ERR_ARGS;
    }
    struct curl_slist *headers = request_headers(session->client, content_type);
    if (!headers) {
        return ERR_MEMORY;
    }

    write_state_t write_state = {
        .response = response,
        .mode = body_mode,
        .max_body = max_body,
        .expected_bytes = expected_download,
        .activity = read_state ? NULL : activity,
    };
    header_state_t header_state = {
        .response = response,
        .upload = read_state,
    };
    configure_common(session, url, &write_state, &header_state, headers);

    if (strcmp(method, "POST") == 0) {
        curl_easy_setopt(session->easy, CURLOPT_POST, 1L);
        if (read_state) {
            curl_easy_setopt(session->easy, CURLOPT_READFUNCTION, read_callback);
            curl_easy_setopt(session->easy, CURLOPT_READDATA, read_state);
            curl_easy_setopt(session->easy, CURLOPT_POSTFIELDSIZE_LARGE,
                             (curl_off_t)read_state->remaining);
        }
    }

    CURLcode code = curl_easy_perform(session->easy);
    if (read_state) {
        activity_end(read_state->activity, &read_state->activity_active);
    }
    int result = finish_request(session, response, &write_state, code);
    curl_slist_free_all(headers);
    return result;
}

int http_global_init(void)
{
    return curl_global_init(CURL_GLOBAL_DEFAULT) == CURLE_OK ? ERR_OK : ERR_NETWORK;
}

void http_global_cleanup(void)
{
    curl_global_cleanup();
}

int http_client_init(http_client_t *client, const char *base_url,
                     const char *access_token, long request_timeout_ms,
                     volatile sig_atomic_t *aborted)
{
    if (!client || !base_url || !*base_url || request_timeout_ms <= 0) {
        return ERR_ARGS;
    }
    memset(client, 0, sizeof(*client));
    size_t len = strlen(base_url);
    while (len > 0 && base_url[len - 1] == '/') {
        len--;
    }
    if (len == 0 || len >= sizeof(client->base_url)) {
        return ERR_ARGS;
    }
    memcpy(client->base_url, base_url, len);
    client->base_url[len] = '\0';
    if (access_token) {
        if (strlen(access_token) >= sizeof(client->access_token)) {
            return ERR_ARGS;
        }
        snprintf(client->access_token, sizeof(client->access_token), "%s", access_token);
    }
    client->request_timeout_ms = request_timeout_ms;
    client->aborted = aborted;
    return ERR_OK;
}

void http_session_init(http_session_t *session, const http_client_t *client)
{
    memset(session, 0, sizeof(*session));
    session->client = client;
    session->easy = curl_easy_init();
}

void http_session_set_cancel(http_session_t *session, atomic_bool *cancel)
{
    if (session) {
        session->request_cancel = cancel;
    }
}

void http_session_cleanup(http_session_t *session)
{
    if (!session) {
        return;
    }
    if (session->easy) {
        curl_easy_cleanup(session->easy);
    }
    memset(session, 0, sizeof(*session));
}

const char *http_session_error(const http_session_t *session)
{
    if (!session || !session->error[0]) {
        return "HTTP transport error";
    }
    return session->error;
}

void http_response_free(http_response_t *response)
{
    if (!response) {
        return;
    }
    free(response->body);
    response->body = NULL;
    response->body_len = 0;
    response->body_capacity = 0;
}

int http_get_json(http_session_t *session, const char *path,
                  size_t max_body, http_response_t *response)
{
    return perform(session, path, "GET", NULL, BODY_BUFFER, max_body, -1, NULL, NULL, response);
}

int http_post_json(http_session_t *session, const char *path,
                   const char *json_body, size_t max_response,
                   http_response_t *response)
{
    response_init(response);
    if (!json_body) {
        return ERR_ARGS;
    }
    char url[MAX_URL_LEN * 2];
    if (join_url(session->client->base_url, path, url, sizeof(url)) != 0) {
        return ERR_ARGS;
    }
    struct curl_slist *headers = request_headers(session->client, "application/json");
    if (!headers) {
        return ERR_MEMORY;
    }
    write_state_t write_state = {
        .response = response,
        .mode = BODY_BUFFER,
        .max_body = max_response,
        .expected_bytes = -1,
    };
    header_state_t header_state = {.response = response};
    configure_common(session, url, &write_state, &header_state, headers);
    curl_easy_setopt(session->easy, CURLOPT_POST, 1L);
    curl_easy_setopt(session->easy, CURLOPT_POSTFIELDS, json_body);
    curl_easy_setopt(session->easy, CURLOPT_POSTFIELDSIZE_LARGE, (curl_off_t)strlen(json_body));
    CURLcode code = curl_easy_perform(session->easy);
    int result = finish_request(session, response, &write_state, code);
    curl_slist_free_all(headers);
    return result;
}

int http_measure_download(http_session_t *session, const char *path,
                          int64_t expected_bytes, const http_activity_t *activity,
                          http_response_t *response)
{
    if (expected_bytes < 0) {
        return ERR_ARGS;
    }
    return perform(session, path, "GET", NULL, BODY_DISCARD, 0, expected_bytes,
                   NULL, activity, response);
}

int http_measure_upload(http_session_t *session, const char *path,
                        int64_t bytes, const http_activity_t *activity,
                        size_t max_response, http_response_t *response)
{
    if (bytes < 0) {
        return ERR_ARGS;
    }
    read_state_t read_state = {
        .remaining = bytes,
        .activity = activity,
    };
    int result = perform(session, path, "POST", "application/octet-stream",
                         BODY_BUFFER, max_response, -1, &read_state, NULL, response);
    if (result == ERR_OK && read_state.transferred != bytes) {
        snprintf(session->error, sizeof(session->error),
                 "HTTP transport consumed %" PRId64 " upload bytes; expected %" PRId64,
                 read_state.transferred, bytes);
        return ERR_PROTOCOL;
    }
    return result;
}

char *http_escape(http_session_t *session, const char *value)
{
    if (!session || !session->easy || !value) {
        return NULL;
    }
    return curl_easy_escape(session->easy, value, 0);
}
