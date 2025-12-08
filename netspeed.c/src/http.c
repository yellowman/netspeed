/*
 * http.c - HTTP client implementation
 *
 * Uses POSIX sockets and OpenSSL/LibreSSL for TLS.
 */

#include "http.h"
#include "timing.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <netdb.h>
#include <arpa/inet.h>

#include <openssl/ssl.h>
#include <openssl/err.h>

/* Initial response buffer size */
#define INITIAL_BUF_SIZE 4096

/* Read buffer for body transfer */
static char read_buf[READ_BUFFER_SIZE];

/* Buffered reader for efficient header parsing (avoids byte-by-byte syscalls) */
#define HDR_BUF_SIZE 8192
static char hdr_buf[HDR_BUF_SIZE];
static size_t hdr_buf_pos = 0;
static size_t hdr_buf_len = 0;

void ssl_init(void)
{
    SSL_library_init();
    SSL_load_error_strings();
    OpenSSL_add_all_algorithms();
}

void ssl_cleanup(void)
{
    EVP_cleanup();
    ERR_free_strings();
}

int url_parse(const char *url_str, url_t *url)
{
    memset(url, 0, sizeof(*url));
    url->port = 80;

    /* Parse scheme */
    const char *p = url_str;
    if (strncmp(p, "https://", 8) == 0) {
        strcpy(url->scheme, "https");
        url->port = 443;
        p += 8;
    } else if (strncmp(p, "http://", 7) == 0) {
        strcpy(url->scheme, "http");
        p += 7;
    } else {
        return -1;
    }

    /* Parse host */
    const char *host_end = p;
    while (*host_end && *host_end != ':' && *host_end != '/') {
        host_end++;
    }

    size_t host_len = host_end - p;
    if (host_len >= MAX_HOSTNAME_LEN) return -1;
    strncpy(url->host, p, host_len);
    p = host_end;

    /* Parse port */
    if (*p == ':') {
        p++;
        url->port = atoi(p);
        while (*p && *p != '/') p++;
    }

    /* Parse path */
    if (*p == '/') {
        strncpy(url->path, p, MAX_URL_LEN - 1);
    } else {
        strcpy(url->path, "/");
    }

    return 0;
}

int http_set_socket_opts(int sockfd)
{
    int yes = 1;

    /* TCP_NODELAY - disable Nagle's algorithm */
    if (setsockopt(sockfd, IPPROTO_TCP, TCP_NODELAY, &yes, sizeof(yes)) < 0) {
        return -1;
    }

    /* Large receive buffer */
    int rcvbuf = READ_BUFFER_SIZE;
    setsockopt(sockfd, SOL_SOCKET, SO_RCVBUF, &rcvbuf, sizeof(rcvbuf));

    /* Large send buffer */
    int sndbuf = WRITE_BUFFER_SIZE;
    setsockopt(sockfd, SOL_SOCKET, SO_SNDBUF, &sndbuf, sizeof(sndbuf));

    return 0;
}

void http_conn_init(http_conn_t *conn)
{
    memset(conn, 0, sizeof(*conn));
    conn->sockfd = -1;
}

int http_connect(http_conn_t *conn, const char *host, int port, bool https)
{
    struct addrinfo hints, *res, *rp;
    char port_str[16];
    int err;

    /* Disconnect if already connected */
    http_disconnect(conn);

    snprintf(port_str, sizeof(port_str), "%d", port);

    memset(&hints, 0, sizeof(hints));
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;

    err = getaddrinfo(host, port_str, &hints, &res);
    if (err != 0) {
        return ERR_DNS;
    }

    /* Try each address */
    for (rp = res; rp != NULL; rp = rp->ai_next) {
        conn->sockfd = socket(rp->ai_family, rp->ai_socktype, rp->ai_protocol);
        if (conn->sockfd < 0) continue;

        /* Set socket options before connect */
        http_set_socket_opts(conn->sockfd);

        /* Set connect timeout */
        struct timeval tv;
        tv.tv_sec = CONNECT_TIMEOUT_SEC;
        tv.tv_usec = 0;
        setsockopt(conn->sockfd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));

        if (connect(conn->sockfd, rp->ai_addr, rp->ai_addrlen) == 0) {
            break;
        }

        close(conn->sockfd);
        conn->sockfd = -1;
    }

    freeaddrinfo(res);

    if (conn->sockfd < 0) {
        return ERR_NETWORK;
    }

    strncpy(conn->host, host, MAX_HOSTNAME_LEN - 1);
    conn->port = port;
    conn->is_https = https;

    /* TLS handshake */
    if (https) {
        conn->ssl_ctx = SSL_CTX_new(TLS_client_method());
        if (!conn->ssl_ctx) {
            close(conn->sockfd);
            conn->sockfd = -1;
            return ERR_TLS;
        }

        conn->ssl = SSL_new(conn->ssl_ctx);
        if (!conn->ssl) {
            SSL_CTX_free(conn->ssl_ctx);
            close(conn->sockfd);
            conn->sockfd = -1;
            return ERR_TLS;
        }

        SSL_set_fd(conn->ssl, conn->sockfd);
        SSL_set_tlsext_host_name(conn->ssl, host);

        if (SSL_connect(conn->ssl) != 1) {
            SSL_free(conn->ssl);
            SSL_CTX_free(conn->ssl_ctx);
            close(conn->sockfd);
            conn->sockfd = -1;
            return ERR_TLS;
        }
    }

    conn->connected = true;
    return ERR_OK;
}

void http_disconnect(http_conn_t *conn)
{
    if (conn->ssl) {
        SSL_shutdown(conn->ssl);
        SSL_free(conn->ssl);
        conn->ssl = NULL;
    }

    if (conn->ssl_ctx) {
        SSL_CTX_free(conn->ssl_ctx);
        conn->ssl_ctx = NULL;
    }

    if (conn->sockfd >= 0) {
        close(conn->sockfd);
        conn->sockfd = -1;
    }

    conn->connected = false;

    /* Clear static header buffer */
    hdr_buf_pos = 0;
    hdr_buf_len = 0;
}

/* Write data to connection */
static ssize_t conn_write(http_conn_t *conn, const void *buf, size_t len)
{
    if (conn->is_https) {
        return SSL_write(conn->ssl, buf, len);
    } else {
        return write(conn->sockfd, buf, len);
    }
}

/* Read data from connection (raw, unbuffered) */
static ssize_t conn_read(http_conn_t *conn, void *buf, size_t len)
{
    if (conn->is_https) {
        return SSL_read(conn->ssl, buf, len);
    } else {
        return read(conn->sockfd, buf, len);
    }
}

/* Clear header buffer (call before each new request) */
static void hdr_buf_clear(void)
{
    hdr_buf_pos = 0;
    hdr_buf_len = 0;
}

/* Read a single byte using buffered I/O (for efficient header parsing) */
static int hdr_read_byte(http_conn_t *conn, char *c)
{
    /* Refill buffer if empty */
    if (hdr_buf_pos >= hdr_buf_len) {
        ssize_t n = conn_read(conn, hdr_buf, HDR_BUF_SIZE);
        if (n <= 0) {
            return -1;
        }
        hdr_buf_pos = 0;
        hdr_buf_len = (size_t)n;
    }

    *c = hdr_buf[hdr_buf_pos++];
    return 0;
}

/* Read from header buffer first, then connection (for body after headers) */
static ssize_t hdr_buf_read(http_conn_t *conn, void *buf, size_t len)
{
    /* If header buffer has leftover data, use it first */
    if (hdr_buf_pos < hdr_buf_len) {
        size_t avail = hdr_buf_len - hdr_buf_pos;
        size_t to_copy = (len < avail) ? len : avail;
        memcpy(buf, hdr_buf + hdr_buf_pos, to_copy);
        hdr_buf_pos += to_copy;
        return (ssize_t)to_copy;
    }

    /* Buffer empty, read directly */
    return conn_read(conn, buf, len);
}

/* Send HTTP request */
static int send_request(http_conn_t *conn, const char *method,
                        const char *path, const void *body, size_t body_len,
                        timing_info_t *timing)
{
    char header[4096];
    int header_len;

    /* Clear header buffer to discard any stale data from previous response */
    hdr_buf_clear();

    /* Headers matching python-requests library defaults */
    if (body && body_len > 0) {
        header_len = snprintf(header, sizeof(header),
            "%s %s HTTP/1.1\r\n"
            "Host: %s\r\n"
            "User-Agent: python-requests/2.32.0\r\n"
            "Accept: */*\r\n"
            "Accept-Encoding: identity\r\n"
            "Connection: keep-alive\r\n"
            "Content-Type: application/octet-stream\r\n"
            "Content-Length: %zu\r\n"
            "\r\n",
            method, path, conn->host, body_len);
    } else {
        header_len = snprintf(header, sizeof(header),
            "%s %s HTTP/1.1\r\n"
            "Host: %s\r\n"
            "User-Agent: python-requests/2.32.0\r\n"
            "Accept: */*\r\n"
            "Accept-Encoding: identity\r\n"
            "Connection: keep-alive\r\n"
            "\r\n",
            method, path, conn->host);
    }

    /* Send headers */
    if (conn_write(conn, header, header_len) != header_len) {
        return ERR_NETWORK;
    }

    /* Mark when we start sending body (for upload timing) */
    timing_mark_wrote_request(timing);

    /* Send body if present */
    if (body && body_len > 0) {
        size_t sent = 0;
        while (sent < body_len) {
            ssize_t n = conn_write(conn, (const char *)body + sent, body_len - sent);
            if (n <= 0) {
                return ERR_NETWORK;
            }
            sent += (size_t)n;
        }
    }

    return ERR_OK;
}

/* Parse HTTP status line */
static int parse_status_line(const char *line, int *status_code)
{
    /* HTTP/1.1 200 OK */
    if (strncmp(line, "HTTP/", 5) != 0) {
        return -1;
    }

    const char *code = strchr(line, ' ');
    if (!code) return -1;

    *status_code = atoi(code + 1);
    return 0;
}

/* Receive HTTP response */
static int receive_response(http_conn_t *conn, http_response_t *response,
                            timing_info_t *timing)
{
    char line[4096];
    size_t line_len = 0;
    bool first_byte_received = false;
    bool in_headers = true;
    int content_length = -1;
    bool chunked = false;

    response->status_code = 0;
    response->body = NULL;
    response->body_len = 0;
    response->body_cap = 0;

    /* Read headers using buffered I/O (much faster than byte-by-byte syscalls) */
    while (in_headers) {
        char c;
        if (hdr_read_byte(conn, &c) < 0) {
            return ERR_NETWORK;
        }

        if (!first_byte_received) {
            timing_mark_got_first_byte(timing);
            first_byte_received = true;
        }

        if (c == '\r') continue;

        if (c == '\n') {
            line[line_len] = '\0';

            if (line_len == 0) {
                /* End of headers */
                in_headers = false;
            } else if (response->status_code == 0) {
                /* Status line */
                if (parse_status_line(line, &response->status_code) < 0) {
                    return ERR_HTTP;
                }
            } else {
                /* Header line */
                if (strncasecmp(line, "Content-Length:", 15) == 0) {
                    content_length = atoi(line + 15);
                } else if (strncasecmp(line, "Transfer-Encoding:", 18) == 0) {
                    if (strstr(line + 18, "chunked")) {
                        chunked = true;
                    }
                }
            }

            line_len = 0;
        } else {
            if (line_len < sizeof(line) - 1) {
                line[line_len++] = c;
            }
        }
    }

    /* Read body */
    if (content_length > 0) {
        size_t body_size = (size_t)content_length;
        response->body = malloc(body_size + 1);
        if (!response->body) {
            return ERR_MEMORY;
        }
        response->body_cap = body_size + 1;

        size_t total = 0;
        while (total < body_size) {
            size_t to_read = body_size - total;
            if (to_read > sizeof(read_buf)) {
                to_read = sizeof(read_buf);
            }

            /* Use hdr_buf_read to drain any leftover header buffer data first */
            ssize_t n = hdr_buf_read(conn, read_buf, to_read);
            if (n <= 0) {
                break;
            }

            memcpy(response->body + total, read_buf, (size_t)n);
            total += (size_t)n;
        }

        response->body_len = total;
        response->body[total] = '\0';
    } else if (content_length == 0) {
        /* Empty body */
        response->body = NULL;
        response->body_len = 0;
    } else if (chunked) {
        /* Chunked transfer - simplified handling */
        response->body = malloc(INITIAL_BUF_SIZE);
        if (!response->body) {
            return ERR_MEMORY;
        }
        response->body_cap = INITIAL_BUF_SIZE;
        response->body_len = 0;

        while (1) {
            /* Read chunk size line using buffered I/O */
            line_len = 0;
            while (1) {
                char c;
                if (hdr_read_byte(conn, &c) < 0) break;
                if (c == '\r') continue;
                if (c == '\n') break;
                if (line_len < sizeof(line) - 1) {
                    line[line_len++] = c;
                }
            }
            line[line_len] = '\0';

            size_t chunk_size = strtoul(line, NULL, 16);
            if (chunk_size == 0) break;

            /* Grow buffer if needed */
            while (response->body_len + chunk_size + 1 > response->body_cap) {
                size_t new_cap = response->body_cap * 2;
                char *new_body = realloc(response->body, new_cap);
                if (!new_body) {
                    /* Keep original buffer for cleanup by caller */
                    return ERR_MEMORY;
                }
                response->body = new_body;
                response->body_cap = new_cap;
            }

            /* Read chunk data */
            size_t read_total = 0;
            while (read_total < chunk_size) {
                size_t to_read = chunk_size - read_total;
                if (to_read > sizeof(read_buf)) {
                    to_read = sizeof(read_buf);
                }

                ssize_t n = hdr_buf_read(conn, read_buf, to_read);
                if (n <= 0) break;

                memcpy(response->body + response->body_len, read_buf, (size_t)n);
                response->body_len += (size_t)n;
                read_total += (size_t)n;
            }

            /* Read trailing CRLF (use hdr_read_byte to handle buffer boundaries) */
            char c;
            if (hdr_read_byte(conn, &c) < 0) break;  /* \r */
            if (hdr_read_byte(conn, &c) < 0) break;  /* \n */
        }

        if (response->body) {
            response->body[response->body_len] = '\0';
        }
    }

    timing_mark_body_done(timing);
    return ERR_OK;
}

int http_get(http_conn_t *conn, const char *path, http_response_t *response)
{
    timing_info_t timing;
    timing_info_init(&timing);

    int err = send_request(conn, "GET", path, NULL, 0, &timing);
    if (err != ERR_OK) return err;

    err = receive_response(conn, response, &timing);
    response->timing = timing;

    return err;
}

int http_post(http_conn_t *conn, const char *path,
              const void *body, size_t body_len,
              http_response_t *response)
{
    timing_info_t timing;
    timing_info_init(&timing);

    int err = send_request(conn, "POST", path, body, body_len, &timing);
    if (err != ERR_OK) return err;

    err = receive_response(conn, response, &timing);
    response->timing = timing;

    return err;
}

void http_response_free(http_response_t *response)
{
    if (response->body) {
        free(response->body);
        response->body = NULL;
    }
    response->body_len = 0;
    response->body_cap = 0;
}
