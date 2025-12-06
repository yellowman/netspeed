/*
 * turn.c - TURN client implementation (RFC 5766)
 *
 * Clean implementation from first principles.
 */

#include "turn.h"
#include "http.h"
#include "json.h"
#include "stats.h"
#include "timing.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <netdb.h>
#include "crypto_compat.h"

/* ===================== Error Handling ===================== */

const char *turn_error_string(turn_error_t err)
{
    switch (err) {
    case TURN_OK: return "Success";
    case TURN_ERR_NETWORK: return "Network error";
    case TURN_ERR_AUTH: return "Authentication failed";
    case TURN_ERR_STALE_NONCE: return "Stale nonce";
    case TURN_ERR_QUOTA: return "Allocation quota reached";
    case TURN_ERR_CAPACITY: return "Server capacity exceeded";
    case TURN_ERR_TIMEOUT: return "Timeout";
    case TURN_ERR_PROTOCOL: return "Protocol error";
    case TURN_ERR_UNSUPPORTED: return "Unsupported";
    default: return "Unknown error";
    }
}

/* ===================== URL Parsing ===================== */

int turn_parse_url(const char *url, char *host, size_t host_len,
                   uint16_t *port, bool *use_tls)
{
    *use_tls = false;
    *port = 3478;

    const char *p = url;

    /* Parse scheme */
    if (strncmp(p, "turns:", 6) == 0) {
        *use_tls = true;
        *port = 5349;
        p += 6;
    } else if (strncmp(p, "turn:", 5) == 0) {
        p += 5;
    } else if (strncmp(p, "stun:", 5) == 0) {
        p += 5;
    } else {
        return -1;
    }

    /* Skip // if present */
    if (strncmp(p, "//", 2) == 0) {
        p += 2;
    }

    /* Find port separator or query string */
    const char *colon = strchr(p, ':');
    const char *question = strchr(p, '?');
    const char *end = question ? question : p + strlen(p);

    if (colon && colon < end) {
        /* Has port */
        size_t host_sz = colon - p;
        if (host_sz >= host_len) host_sz = host_len - 1;
        memcpy(host, p, host_sz);
        host[host_sz] = '\0';
        *port = atoi(colon + 1);
    } else {
        /* No port */
        size_t host_sz = end - p;
        if (host_sz >= host_len) host_sz = host_len - 1;
        memcpy(host, p, host_sz);
        host[host_sz] = '\0';
    }

    return 0;
}

/* ===================== Credentials ===================== */

int turn_fetch_credentials(const char *base_url, turn_creds_t *creds)
{
    memset(creds, 0, sizeof(*creds));

    /* Build URL */
    char url[MAX_URL_LEN];
    snprintf(url, sizeof(url), "%s/api/turn/credentials", base_url);

    /* Parse URL */
    url_t parsed;
    if (url_parse(url, &parsed) < 0) {
        return -1;
    }

    /* Connect */
    http_conn_t conn;
    http_conn_init(&conn);
    int err = http_connect(&conn, parsed.host, parsed.port,
                           strcmp(parsed.scheme, "https") == 0);
    if (err != 0) {
        return err;
    }

    /* GET request */
    http_response_t resp;
    char path[512];
    snprintf(path, sizeof(path), "/api/turn/credentials");
    err = http_get(&conn, path, &resp);
    http_disconnect(&conn);

    if (err != 0 || resp.status_code != 200) {
        http_response_free(&resp);
        return TURN_ERR_NETWORK;
    }

    /* Parse JSON response */
    json_value_t *root = json_parse(resp.body);
    http_response_free(&resp);

    if (!root) {
        return TURN_ERR_PROTOCOL;
    }

    const char *s;
    if ((s = json_get_string(root, "username"))) {
        strncpy(creds->username, s, sizeof(creds->username) - 1);
    }
    if ((s = json_get_string(root, "credential"))) {
        strncpy(creds->credential, s, sizeof(creds->credential) - 1);
    }
    creds->ttl_sec = json_get_int(root, "ttlSec", 600);

    /* Parse servers array */
    json_value_t *servers = json_get(root, "servers");
    if (servers && servers->type == JSON_ARRAY) {
        json_element_t *elem = servers->u.array;
        while (elem && creds->server_count < 8) {
            if (elem->value && elem->value->type == JSON_STRING) {
                strncpy(creds->servers[creds->server_count],
                        elem->value->u.string,
                        sizeof(creds->servers[0]) - 1);
                creds->server_count++;
            }
            elem = elem->next;
        }
    }

    json_free(root);
    return TURN_OK;
}

/* ===================== STUN Message Building ===================== */

void stun_generate_txn_id(uint8_t txn_id[12])
{
    crypto_random_bytes(txn_id, 12);
}

size_t stun_build_header(uint8_t *buf, uint16_t msg_type, const uint8_t txn_id[12])
{
    /* Message Type */
    buf[0] = (msg_type >> 8) & 0xFF;
    buf[1] = msg_type & 0xFF;

    /* Message Length (placeholder) */
    buf[2] = 0;
    buf[3] = 0;

    /* Magic Cookie */
    buf[4] = 0x21;
    buf[5] = 0x12;
    buf[6] = 0xA4;
    buf[7] = 0x42;

    /* Transaction ID */
    memcpy(buf + 8, txn_id, 12);

    return 20;
}

size_t stun_add_string_attr(uint8_t *buf, size_t offset, uint16_t type, const char *str)
{
    uint16_t len = strlen(str);
    uint16_t padded_len = (len + 3) & ~3; /* Pad to 4-byte boundary */

    buf[offset] = (type >> 8) & 0xFF;
    buf[offset + 1] = type & 0xFF;
    buf[offset + 2] = (len >> 8) & 0xFF;
    buf[offset + 3] = len & 0xFF;
    memcpy(buf + offset + 4, str, len);

    /* Zero padding */
    memset(buf + offset + 4 + len, 0, padded_len - len);

    return 4 + padded_len;
}

size_t stun_add_uint32_attr(uint8_t *buf, size_t offset, uint16_t type, uint32_t value)
{
    buf[offset] = (type >> 8) & 0xFF;
    buf[offset + 1] = type & 0xFF;
    buf[offset + 2] = 0;
    buf[offset + 3] = 4;
    buf[offset + 4] = (value >> 24) & 0xFF;
    buf[offset + 5] = (value >> 16) & 0xFF;
    buf[offset + 6] = (value >> 8) & 0xFF;
    buf[offset + 7] = value & 0xFF;
    return 8;
}

size_t stun_add_requested_transport(uint8_t *buf, size_t offset, uint8_t protocol)
{
    buf[offset] = (ATTR_REQUESTED_TRANSPORT >> 8) & 0xFF;
    buf[offset + 1] = ATTR_REQUESTED_TRANSPORT & 0xFF;
    buf[offset + 2] = 0;
    buf[offset + 3] = 4;
    buf[offset + 4] = protocol; /* 17 = UDP */
    buf[offset + 5] = 0;
    buf[offset + 6] = 0;
    buf[offset + 7] = 0;
    return 8;
}

size_t stun_add_xor_peer_address(uint8_t *buf, size_t offset, uint32_t addr,
                                  uint16_t port, const uint8_t txn_id[12])
{
    (void)txn_id; /* Only used for IPv6 */

    uint16_t xport = port ^ (STUN_MAGIC_COOKIE >> 16);
    uint32_t xaddr = addr ^ STUN_MAGIC_COOKIE;

    buf[offset] = (ATTR_XOR_PEER_ADDRESS >> 8) & 0xFF;
    buf[offset + 1] = ATTR_XOR_PEER_ADDRESS & 0xFF;
    buf[offset + 2] = 0;
    buf[offset + 3] = 8;
    buf[offset + 4] = 0;        /* Reserved */
    buf[offset + 5] = 0x01;     /* IPv4 */
    buf[offset + 6] = (xport >> 8) & 0xFF;
    buf[offset + 7] = xport & 0xFF;
    buf[offset + 8] = (xaddr >> 24) & 0xFF;
    buf[offset + 9] = (xaddr >> 16) & 0xFF;
    buf[offset + 10] = (xaddr >> 8) & 0xFF;
    buf[offset + 11] = xaddr & 0xFF;

    return 12;
}

size_t stun_add_channel_number(uint8_t *buf, size_t offset, uint16_t channel)
{
    buf[offset] = (ATTR_CHANNEL_NUMBER >> 8) & 0xFF;
    buf[offset + 1] = ATTR_CHANNEL_NUMBER & 0xFF;
    buf[offset + 2] = 0;
    buf[offset + 3] = 4;
    buf[offset + 4] = (channel >> 8) & 0xFF;
    buf[offset + 5] = channel & 0xFF;
    buf[offset + 6] = 0;
    buf[offset + 7] = 0;
    return 8;
}

void turn_compute_key(const char *username, const char *realm,
                      const char *password, uint8_t key[16])
{
    char concat[512];
    snprintf(concat, sizeof(concat), "%s:%s:%s", username, realm, password);

    /* Use compatibility wrapper for MD5 (works with OpenSSL and LibreSSL) */
    crypto_md5(concat, strlen(concat), key);
}

size_t stun_add_message_integrity(uint8_t *buf, size_t len, const uint8_t key[16])
{
    /* Update message length to include MESSAGE-INTEGRITY */
    uint16_t new_len = (len - 20) + 24; /* +24 for the attribute */
    buf[2] = (new_len >> 8) & 0xFF;
    buf[3] = new_len & 0xFF;

    /* Compute HMAC-SHA1 using compatibility wrapper */
    unsigned char hmac[20];
    crypto_hmac_sha1(key, 16, buf, len, hmac);

    /* Append MESSAGE-INTEGRITY attribute */
    buf[len] = 0x00;
    buf[len + 1] = 0x08; /* MESSAGE-INTEGRITY type */
    buf[len + 2] = 0x00;
    buf[len + 3] = 0x14; /* length = 20 */
    memcpy(buf + len + 4, hmac, 20);

    return 24;
}

void stun_update_length(uint8_t *buf, size_t len)
{
    uint16_t msg_len = len - 20;
    buf[2] = (msg_len >> 8) & 0xFF;
    buf[3] = msg_len & 0xFF;
}

/* ===================== STUN Message Parsing ===================== */

int stun_parse_header(const uint8_t *buf, size_t len, uint16_t *msg_len,
                      uint8_t txn_id[12])
{
    if (len < 20) return -1;

    /* Check magic cookie */
    if (buf[4] != 0x21 || buf[5] != 0x12 || buf[6] != 0xA4 || buf[7] != 0x42) {
        return -1;
    }

    uint16_t msg_type = (buf[0] << 8) | buf[1];
    *msg_len = (buf[2] << 8) | buf[3];

    if (txn_id) {
        memcpy(txn_id, buf + 8, 12);
    }

    return msg_type;
}

const uint8_t *stun_find_attr(const uint8_t *msg, size_t msg_len,
                               uint16_t attr_type, uint16_t *attr_len)
{
    if (msg_len < 20) return NULL;

    size_t offset = 20;
    size_t body_len = (msg[2] << 8) | msg[3];
    size_t end = 20 + body_len;
    if (end > msg_len) end = msg_len;

    while (offset + 4 <= end) {
        uint16_t type = (msg[offset] << 8) | msg[offset + 1];
        uint16_t len = (msg[offset + 2] << 8) | msg[offset + 3];
        uint16_t padded_len = (len + 3) & ~3;

        if (type == attr_type) {
            *attr_len = len;
            return msg + offset + 4;
        }

        offset += 4 + padded_len;
    }

    return NULL;
}

int stun_parse_xor_address(const uint8_t *attr, uint16_t attr_len,
                           const uint8_t txn_id[12],
                           uint32_t *addr, uint16_t *port)
{
    (void)txn_id;
    if (attr_len < 8) return -1;

    uint8_t family = attr[1];
    if (family != 0x01) return -1; /* Only IPv4 supported */

    uint16_t xport = (attr[2] << 8) | attr[3];
    *port = xport ^ (STUN_MAGIC_COOKIE >> 16);

    uint32_t xaddr = (attr[4] << 24) | (attr[5] << 16) | (attr[6] << 8) | attr[7];
    *addr = xaddr ^ STUN_MAGIC_COOKIE;

    return 0;
}

int stun_parse_error_code(const uint8_t *attr, uint16_t attr_len,
                          int *code, char *reason, size_t reason_len)
{
    if (attr_len < 4) return -1;

    int class = attr[2] & 0x07;
    int number = attr[3];
    *code = class * 100 + number;

    if (reason && reason_len > 0 && attr_len > 4) {
        size_t copy_len = attr_len - 4;
        if (copy_len >= reason_len) copy_len = reason_len - 1;
        memcpy(reason, attr + 4, copy_len);
        reason[copy_len] = '\0';
    }

    return 0;
}

int stun_parse_string_attr(const uint8_t *attr, uint16_t attr_len,
                           char *str, size_t str_len)
{
    size_t copy_len = attr_len;
    if (copy_len >= str_len) copy_len = str_len - 1;
    memcpy(str, attr, copy_len);
    str[copy_len] = '\0';
    return 0;
}

/* ===================== TURN Connection ===================== */

int turn_init(turn_conn_t *conn, const char *server_url,
              const char *username, const char *credential)
{
    memset(conn, 0, sizeof(*conn));

    /* Parse server URL */
    bool use_tls;
    if (turn_parse_url(server_url, conn->server_host, sizeof(conn->server_host),
                       &conn->server_port, &use_tls) < 0) {
        return TURN_ERR_PROTOCOL;
    }

    /* TLS over UDP (DTLS) not implemented - use non-TLS */
    if (use_tls) {
        return TURN_ERR_UNSUPPORTED;
    }

    strncpy(conn->username, username, sizeof(conn->username) - 1);
    strncpy(conn->credential, credential, sizeof(conn->credential) - 1);

    /* Resolve server address */
    struct hostent *he = gethostbyname(conn->server_host);
    if (!he) {
        return TURN_ERR_NETWORK;
    }

    conn->server_addr.sin_family = AF_INET;
    conn->server_addr.sin_port = htons(conn->server_port);
    memcpy(&conn->server_addr.sin_addr, he->h_addr_list[0], he->h_length);

    /* Create UDP socket */
    conn->sock = socket(AF_INET, SOCK_DGRAM, 0);
    if (conn->sock < 0) {
        return TURN_ERR_NETWORK;
    }

    /* Set non-blocking */
    int flags = fcntl(conn->sock, F_GETFL, 0);
    fcntl(conn->sock, F_SETFL, flags | O_NONBLOCK);

    /* Connect socket to server (for send/recv convenience) */
    if (connect(conn->sock, (struct sockaddr *)&conn->server_addr,
                sizeof(conn->server_addr)) < 0) {
        close(conn->sock);
        return TURN_ERR_NETWORK;
    }

    stun_generate_txn_id(conn->txn_id);

    return TURN_OK;
}

int turn_allocate(turn_conn_t *conn)
{
    uint8_t req[512];
    uint8_t resp[2048];
    size_t len;

    /* Step 1: Unauthenticated allocate request */
    len = stun_build_header(req, TURN_ALLOCATE_REQUEST, conn->txn_id);
    len += stun_add_requested_transport(req, len, 17); /* UDP */
    stun_update_length(req, len);

    if (send(conn->sock, req, len, 0) < 0) {
        return TURN_ERR_NETWORK;
    }

    /* Wait for response with timeout */
    struct pollfd pfd = {.fd = conn->sock, .events = POLLIN};
    if (poll(&pfd, 1, 5000) <= 0) {
        return TURN_ERR_TIMEOUT;
    }

    ssize_t n = recv(conn->sock, resp, sizeof(resp), 0);
    if (n < 20) {
        return TURN_ERR_PROTOCOL;
    }

    /* Parse response */
    uint16_t msg_len;
    int msg_type = stun_parse_header(resp, n, &msg_len, NULL);

    if (msg_type == TURN_ALLOCATE_ERROR) {
        /* Extract REALM and NONCE from 401 response */
        uint16_t attr_len;
        const uint8_t *attr;

        attr = stun_find_attr(resp, n, ATTR_REALM, &attr_len);
        if (attr) {
            stun_parse_string_attr(attr, attr_len, conn->realm, sizeof(conn->realm));
        }

        attr = stun_find_attr(resp, n, ATTR_NONCE, &attr_len);
        if (attr) {
            stun_parse_string_attr(attr, attr_len, conn->nonce, sizeof(conn->nonce));
        }

        /* Check error code */
        attr = stun_find_attr(resp, n, ATTR_ERROR_CODE, &attr_len);
        if (attr) {
            int code;
            char reason[256];
            stun_parse_error_code(attr, attr_len, &code, reason, sizeof(reason));
            if (code != 401) {
                return TURN_ERR_AUTH;
            }
        }
    } else {
        return TURN_ERR_PROTOCOL;
    }

    /* Step 2: Authenticated allocate request */
    turn_compute_key(conn->username, conn->realm, conn->credential, conn->key);
    stun_generate_txn_id(conn->txn_id);

    len = stun_build_header(req, TURN_ALLOCATE_REQUEST, conn->txn_id);
    len += stun_add_string_attr(req, len, ATTR_USERNAME, conn->username);
    len += stun_add_string_attr(req, len, ATTR_REALM, conn->realm);
    len += stun_add_string_attr(req, len, ATTR_NONCE, conn->nonce);
    len += stun_add_requested_transport(req, len, 17);
    len += stun_add_message_integrity(req, len, conn->key);
    stun_update_length(req, len);

    if (send(conn->sock, req, len, 0) < 0) {
        return TURN_ERR_NETWORK;
    }

    /* Wait for response */
    if (poll(&pfd, 1, 5000) <= 0) {
        return TURN_ERR_TIMEOUT;
    }

    n = recv(conn->sock, resp, sizeof(resp), 0);
    if (n < 20) {
        return TURN_ERR_PROTOCOL;
    }

    msg_type = stun_parse_header(resp, n, &msg_len, NULL);

    if (msg_type == TURN_ALLOCATE_RESPONSE) {
        /* Extract XOR-RELAYED-ADDRESS */
        uint16_t attr_len;
        const uint8_t *attr = stun_find_attr(resp, n, ATTR_XOR_RELAYED_ADDRESS, &attr_len);
        if (attr) {
            stun_parse_xor_address(attr, attr_len, conn->txn_id,
                                   &conn->relay_addr, &conn->relay_port);
        }

        /* Extract LIFETIME */
        attr = stun_find_attr(resp, n, ATTR_LIFETIME, &attr_len);
        if (attr && attr_len >= 4) {
            conn->lifetime_sec = (attr[0] << 24) | (attr[1] << 16) |
                                 (attr[2] << 8) | attr[3];
        } else {
            conn->lifetime_sec = 600;
        }

        conn->allocated = true;
        conn->alloc_time = time(NULL);
        return TURN_OK;
    } else if (msg_type == TURN_ALLOCATE_ERROR) {
        uint16_t attr_len;
        const uint8_t *attr = stun_find_attr(resp, n, ATTR_ERROR_CODE, &attr_len);
        if (attr) {
            int code;
            stun_parse_error_code(attr, attr_len, &code, NULL, 0);
            if (code == 438) return TURN_ERR_STALE_NONCE;
            if (code == 486) return TURN_ERR_QUOTA;
            if (code == 508) return TURN_ERR_CAPACITY;
        }
        return TURN_ERR_AUTH;
    }

    return TURN_ERR_PROTOCOL;
}

int turn_create_permission(turn_conn_t *conn, uint32_t peer_addr, uint16_t peer_port)
{
    uint8_t req[512];
    uint8_t resp[512];

    stun_generate_txn_id(conn->txn_id);

    size_t len = stun_build_header(req, TURN_CREATE_PERM_REQUEST, conn->txn_id);
    len += stun_add_xor_peer_address(req, len, peer_addr, peer_port, conn->txn_id);
    len += stun_add_string_attr(req, len, ATTR_USERNAME, conn->username);
    len += stun_add_string_attr(req, len, ATTR_REALM, conn->realm);
    len += stun_add_string_attr(req, len, ATTR_NONCE, conn->nonce);
    len += stun_add_message_integrity(req, len, conn->key);
    stun_update_length(req, len);

    if (send(conn->sock, req, len, 0) < 0) {
        return TURN_ERR_NETWORK;
    }

    struct pollfd pfd = {.fd = conn->sock, .events = POLLIN};
    if (poll(&pfd, 1, 5000) <= 0) {
        return TURN_ERR_TIMEOUT;
    }

    ssize_t n = recv(conn->sock, resp, sizeof(resp), 0);
    if (n < 20) {
        return TURN_ERR_PROTOCOL;
    }

    uint16_t msg_len;
    int msg_type = stun_parse_header(resp, n, &msg_len, NULL);

    return (msg_type == TURN_CREATE_PERM_RESPONSE) ? TURN_OK : TURN_ERR_PROTOCOL;
}

int turn_bind_channel(turn_conn_t *conn, uint32_t peer_addr, uint16_t peer_port)
{
    conn->channel = 0x4000; /* First channel number */
    conn->peer_addr = peer_addr;
    conn->peer_port = peer_port;

    uint8_t req[512];
    uint8_t resp[512];

    stun_generate_txn_id(conn->txn_id);

    size_t len = stun_build_header(req, TURN_CHANNEL_BIND_REQUEST, conn->txn_id);
    len += stun_add_channel_number(req, len, conn->channel);
    len += stun_add_xor_peer_address(req, len, peer_addr, peer_port, conn->txn_id);
    len += stun_add_string_attr(req, len, ATTR_USERNAME, conn->username);
    len += stun_add_string_attr(req, len, ATTR_REALM, conn->realm);
    len += stun_add_string_attr(req, len, ATTR_NONCE, conn->nonce);
    len += stun_add_message_integrity(req, len, conn->key);
    stun_update_length(req, len);

    if (send(conn->sock, req, len, 0) < 0) {
        return TURN_ERR_NETWORK;
    }

    struct pollfd pfd = {.fd = conn->sock, .events = POLLIN};
    if (poll(&pfd, 1, 5000) <= 0) {
        return TURN_ERR_TIMEOUT;
    }

    ssize_t n = recv(conn->sock, resp, sizeof(resp), 0);
    if (n < 20) {
        return TURN_ERR_PROTOCOL;
    }

    uint16_t msg_len;
    int msg_type = stun_parse_header(resp, n, &msg_len, NULL);

    if (msg_type == TURN_CHANNEL_BIND_RESPONSE) {
        conn->channel_bound = true;
        return TURN_OK;
    }

    return TURN_ERR_PROTOCOL;
}

int turn_send(turn_conn_t *conn, const void *data, size_t len)
{
    if (!conn->channel_bound) {
        return TURN_ERR_PROTOCOL;
    }

    /* Channel data format: 4-byte header + data */
    uint8_t buf[4 + len];
    size_t padded_len = (len + 3) & ~3;

    buf[0] = (conn->channel >> 8) & 0xFF;
    buf[1] = conn->channel & 0xFF;
    buf[2] = (len >> 8) & 0xFF;
    buf[3] = len & 0xFF;
    memcpy(buf + 4, data, len);

    ssize_t n = send(conn->sock, buf, 4 + padded_len, 0);
    return (n > 0) ? TURN_OK : TURN_ERR_NETWORK;
}

bool turn_poll_readable(turn_conn_t *conn, int timeout_ms)
{
    struct pollfd pfd = {.fd = conn->sock, .events = POLLIN};
    return poll(&pfd, 1, timeout_ms) > 0;
}

ssize_t turn_recv(turn_conn_t *conn, void *data, size_t max_len, int timeout_ms)
{
    if (!turn_poll_readable(conn, timeout_ms)) {
        return 0;
    }

    uint8_t buf[4 + max_len];
    ssize_t n = recv(conn->sock, buf, sizeof(buf), 0);
    if (n < 4) {
        return -1;
    }

    /* Check if this is channel data or STUN message */
    if ((buf[0] & 0xC0) == 0x40) {
        /* Channel data */
        uint16_t len = (buf[2] << 8) | buf[3];
        if (len > max_len) len = max_len;
        memcpy(data, buf + 4, len);
        return len;
    } else if ((buf[0] & 0xC0) == 0x00) {
        /* STUN message - could be Data indication */
        if (n >= 20) {
            uint16_t attr_len;
            const uint8_t *attr = stun_find_attr(buf, n, ATTR_DATA, &attr_len);
            if (attr) {
                size_t copy_len = attr_len;
                if (copy_len > max_len) copy_len = max_len;
                memcpy(data, attr, copy_len);
                return copy_len;
            }
        }
    }

    return -1;
}

int turn_refresh(turn_conn_t *conn)
{
    uint8_t req[512];
    uint8_t resp[512];

    stun_generate_txn_id(conn->txn_id);

    size_t len = stun_build_header(req, TURN_REFRESH_REQUEST, conn->txn_id);
    len += stun_add_uint32_attr(req, len, ATTR_LIFETIME, 600);
    len += stun_add_string_attr(req, len, ATTR_USERNAME, conn->username);
    len += stun_add_string_attr(req, len, ATTR_REALM, conn->realm);
    len += stun_add_string_attr(req, len, ATTR_NONCE, conn->nonce);
    len += stun_add_message_integrity(req, len, conn->key);
    stun_update_length(req, len);

    if (send(conn->sock, req, len, 0) < 0) {
        return TURN_ERR_NETWORK;
    }

    struct pollfd pfd = {.fd = conn->sock, .events = POLLIN};
    if (poll(&pfd, 1, 5000) <= 0) {
        return TURN_ERR_TIMEOUT;
    }

    ssize_t n = recv(conn->sock, resp, sizeof(resp), 0);
    if (n < 20) {
        return TURN_ERR_PROTOCOL;
    }

    uint16_t msg_len;
    int msg_type = stun_parse_header(resp, n, &msg_len, NULL);

    if (msg_type == TURN_REFRESH_RESPONSE) {
        conn->alloc_time = time(NULL);
        return TURN_OK;
    }

    return TURN_ERR_PROTOCOL;
}

void turn_close(turn_conn_t *conn)
{
    if (conn->sock > 0) {
        close(conn->sock);
        conn->sock = -1;
    }
    conn->allocated = false;
    conn->channel_bound = false;
}

/* ===================== Packet Loss Test ===================== */

/* Packet message structure */
typedef struct {
    uint32_t seq;
    int64_t sent_at_ms;
} __attribute__((packed)) pkt_msg_t;

typedef struct {
    uint32_t seq;
    int64_t sent_at_ms;
    int64_t recv_at_ms;
} __attribute__((packed)) ack_msg_t;

int turn_run_packet_loss_test(const char *base_url, packet_loss_result_t *result)
{
    memset(result, 0, sizeof(*result));

    /* Fetch TURN credentials */
    turn_creds_t creds;
    int err = turn_fetch_credentials(base_url, &creds);
    if (err != TURN_OK || creds.server_count == 0) {
        result->unavailable = true;
        strncpy(result->reason, "TURN credentials unavailable",
                sizeof(result->reason) - 1);
        return TURN_OK;
    }

    /* Find a UDP TURN server */
    char *turn_url = NULL;
    for (int i = 0; i < creds.server_count; i++) {
        if (strstr(creds.servers[i], "turn:") &&
            strstr(creds.servers[i], "transport=udp")) {
            turn_url = creds.servers[i];
            break;
        }
        if (strstr(creds.servers[i], "turn:") && !strstr(creds.servers[i], "transport=")) {
            turn_url = creds.servers[i]; /* Default is UDP */
            break;
        }
    }

    if (!turn_url) {
        result->unavailable = true;
        strncpy(result->reason, "No UDP TURN server available",
                sizeof(result->reason) - 1);
        return TURN_OK;
    }

    /* Initialize TURN connection */
    turn_conn_t conn;
    err = turn_init(&conn, turn_url, creds.username, creds.credential);
    if (err != TURN_OK) {
        result->unavailable = true;
        snprintf(result->reason, sizeof(result->reason), "TURN init failed: %s",
                 turn_error_string(err));
        return TURN_OK;
    }

    /* Allocate relay */
    err = turn_allocate(&conn);
    if (err != TURN_OK) {
        turn_close(&conn);
        result->unavailable = true;
        snprintf(result->reason, sizeof(result->reason), "TURN allocate failed: %s",
                 turn_error_string(err));
        return TURN_OK;
    }

    /*
     * Note: Full WebRTC packet loss requires signaling with server to get peer address.
     * This simplified implementation would need the server to support a custom protocol.
     * For now, mark as unavailable with reason.
     */
    turn_close(&conn);
    result->unavailable = true;
    strncpy(result->reason, "Full WebRTC not implemented in C client - use Go client",
            sizeof(result->reason) - 1);

    return TURN_OK;
}
