/*
 * http.h - libcurl-based HTTP/1.1 transport for the protocol-v2 C client.
 */
#ifndef NETSPEED_HTTP_H
#define NETSPEED_HTTP_H

#include "types.h"

#include <curl/curl.h>
#include <signal.h>
#include <stdatomic.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    char base_url[MAX_URL_LEN];
    char access_token[MAX_TOKEN_LEN];
    long request_timeout_ms;
    volatile sig_atomic_t *aborted;
} http_client_t;

typedef struct {
    const http_client_t *client;
    CURL *easy;
    char error[CURL_ERROR_SIZE];
    atomic_bool *request_cancel;
} http_session_t;

typedef struct {
    int status_code;
    char content_type[128];
    char cache_control[256];
    char content_encoding[64];
    char transfer_encoding[128];
    char x_accel_buffering[64];
    char measurement[32];
    char payload[32];
    char framing[32];
    char upload_content_encoding[32];
    char flush[16];
    char http_protocol[16];
    int64_t content_length;
    int64_t chunk_bytes;
    int64_t expected_upload_bytes;
    int64_t accepted_upload_bytes;
    int64_t upload_duration_ns;
    char *body;
    size_t body_len;
    size_t body_capacity;
    int64_t transferred_bytes;
    double request_to_first_byte_ms;
    double body_duration_ms;
    char timing_source[48];
    bool connection_reused;
    long new_connections;
} http_response_t;

typedef struct {
    void (*begin)(void *opaque);
    void (*end)(void *opaque);
    void *opaque;
} http_activity_t;

int http_global_init(void);
void http_global_cleanup(void);

int http_client_init(http_client_t *client, const char *base_url,
                     const char *access_token, long request_timeout_ms,
                     volatile sig_atomic_t *aborted);
void http_session_init(http_session_t *session, const http_client_t *client);
void http_session_cleanup(http_session_t *session);
void http_session_set_cancel(http_session_t *session, atomic_bool *cancel);
const char *http_session_error(const http_session_t *session);
void http_response_free(http_response_t *response);

int http_get_json(http_session_t *session, const char *path,
                  size_t max_body, http_response_t *response);
int http_post_json(http_session_t *session, const char *path,
                   const char *json_body, size_t max_response,
                   http_response_t *response);
int http_measure_download(http_session_t *session, const char *path,
                          int64_t expected_bytes, const http_activity_t *activity,
                          http_response_t *response);
int http_measure_empty(http_session_t *session, const char *path,
                       const char *method, http_response_t *response);
int http_measure_upload(http_session_t *session, const char *path,
                        int64_t bytes, const http_activity_t *activity,
                        size_t max_response, http_response_t *response);

/* Percent-encode a query component. The caller owns the returned buffer. */
char *http_escape(http_session_t *session, const char *value);

#ifdef __cplusplus
}
#endif

#endif /* NETSPEED_HTTP_H */
