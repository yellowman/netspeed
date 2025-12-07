/*
 * http.h - HTTP client interface
 *
 * Provides HTTP/1.1 client with TLS support for speed testing.
 * Uses OpenSSL/LibreSSL for TLS.
 */

#ifndef NETSPEED_HTTP_H
#define NETSPEED_HTTP_H

#include "types.h"
#include <openssl/ssl.h>

/* HTTP response */
typedef struct {
    int status_code;
    char *body;
    size_t body_len;
    size_t body_cap;
    timing_info_t timing;
} http_response_t;

/* Read buffer size for header parsing (64KB for large headers) */
#define CONN_READ_BUF_SIZE 65536

/* HTTP connection (persistent) */
typedef struct {
    int sockfd;
    SSL_CTX *ssl_ctx;
    SSL *ssl;
    char host[MAX_HOSTNAME_LEN];
    int port;
    bool is_https;
    bool connected;
    /* Buffered reader for efficient header parsing */
    char read_buf[CONN_READ_BUF_SIZE];
    size_t read_pos;   /* Current position in buffer */
    size_t read_len;   /* Amount of data in buffer */
} http_conn_t;

/* URL components */
typedef struct {
    char scheme[8];
    char host[MAX_HOSTNAME_LEN];
    int port;
    char path[MAX_URL_LEN];
} url_t;

/*
 * Parse URL into components.
 * Returns 0 on success, -1 on error.
 */
int url_parse(const char *url_str, url_t *url);

/*
 * Initialize HTTP connection.
 * Does not connect yet - use http_connect().
 */
void http_conn_init(http_conn_t *conn);

/*
 * Connect to server.
 * Returns 0 on success, error code on failure.
 */
int http_connect(http_conn_t *conn, const char *host, int port, bool https);

/*
 * Disconnect and cleanup.
 */
void http_disconnect(http_conn_t *conn);

/*
 * HTTP GET request with precise timing.
 * Caller must free response->body if not NULL.
 * Returns 0 on success, error code on failure.
 */
int http_get(http_conn_t *conn, const char *path, http_response_t *response);

/*
 * HTTP POST request with precise timing.
 * Caller must free response->body if not NULL.
 * Returns 0 on success, error code on failure.
 */
int http_post(http_conn_t *conn, const char *path,
              const void *body, size_t body_len,
              http_response_t *response);

/*
 * Free response body.
 */
void http_response_free(http_response_t *response);

/*
 * Set socket options for high-speed transfers.
 * - TCP_NODELAY
 * - Large send/receive buffers
 */
int http_set_socket_opts(int sockfd);

/*
 * Global SSL initialization (call once at startup).
 */
void ssl_init(void);

/*
 * Global SSL cleanup (call once at shutdown).
 */
void ssl_cleanup(void);

#endif /* NETSPEED_HTTP_H */
