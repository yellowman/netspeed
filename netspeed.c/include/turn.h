/*
 * turn.h - TURN client implementation (RFC 5766)
 *
 * Implements TURN protocol from first principles for packet loss testing.
 */

#ifndef NETSPEED_TURN_H
#define NETSPEED_TURN_H

#include "types.h"
#include <stdint.h>
#include <stdbool.h>
#include <time.h>
#include <netinet/in.h>

/* STUN/TURN constants */
#define STUN_MAGIC_COOKIE   0x2112A442

/* Message types (method | class) */
#define STUN_BINDING_REQUEST       0x0001
#define STUN_BINDING_RESPONSE      0x0101
#define TURN_ALLOCATE_REQUEST      0x0003
#define TURN_ALLOCATE_RESPONSE     0x0103
#define TURN_ALLOCATE_ERROR        0x0113
#define TURN_REFRESH_REQUEST       0x0004
#define TURN_REFRESH_RESPONSE      0x0104
#define TURN_SEND_INDICATION       0x0016
#define TURN_DATA_INDICATION       0x0017
#define TURN_CREATE_PERM_REQUEST   0x0008
#define TURN_CREATE_PERM_RESPONSE  0x0108
#define TURN_CHANNEL_BIND_REQUEST  0x0009
#define TURN_CHANNEL_BIND_RESPONSE 0x0109

/* Attribute types */
#define ATTR_MAPPED_ADDRESS        0x0001
#define ATTR_USERNAME              0x0006
#define ATTR_MESSAGE_INTEGRITY     0x0008
#define ATTR_ERROR_CODE            0x0009
#define ATTR_REALM                 0x0014
#define ATTR_NONCE                 0x0015
#define ATTR_XOR_RELAYED_ADDRESS   0x0016
#define ATTR_REQUESTED_TRANSPORT   0x0019
#define ATTR_XOR_PEER_ADDRESS      0x0012
#define ATTR_DATA                  0x0013
#define ATTR_CHANNEL_NUMBER        0x000C
#define ATTR_LIFETIME              0x000D
#define ATTR_XOR_MAPPED_ADDRESS    0x0020
#define ATTR_SOFTWARE              0x8022
#define ATTR_FINGERPRINT           0x8028

/* TURN error codes */
typedef enum {
    TURN_OK = 0,
    TURN_ERR_NETWORK = -1,
    TURN_ERR_AUTH = -2,
    TURN_ERR_STALE_NONCE = -3,
    TURN_ERR_QUOTA = -4,
    TURN_ERR_CAPACITY = -5,
    TURN_ERR_TIMEOUT = -6,
    TURN_ERR_PROTOCOL = -7,
    TURN_ERR_UNSUPPORTED = -8,
} turn_error_t;

/* TURN credentials (from /api/turn/credentials) */
typedef struct {
    char username[128];
    char credential[256];
    int ttl_sec;
    char servers[8][256];
    int server_count;
} turn_creds_t;

/* TURN connection state */
typedef struct {
    int sock;                       /* UDP socket */
    char server_host[256];          /* TURN server hostname */
    uint16_t server_port;           /* TURN server port */
    struct sockaddr_in server_addr; /* Resolved server address */

    /* Authentication */
    char username[128];
    char credential[256];
    char realm[128];
    char nonce[256];
    uint8_t key[16];                /* MD5(user:realm:pass) */

    /* Allocation state */
    bool allocated;
    uint32_t relay_addr;            /* Allocated relay IPv4 */
    uint16_t relay_port;
    uint32_t lifetime_sec;
    time_t alloc_time;

    /* Channel binding */
    uint16_t channel;               /* 0x4000+ */
    uint32_t peer_addr;
    uint16_t peer_port;
    bool channel_bound;

    /* Transaction tracking */
    uint8_t txn_id[12];
} turn_conn_t;

/*
 * Parse TURN server URL.
 * Format: turn:host:port or turns:host:port
 * Returns 0 on success.
 */
int turn_parse_url(const char *url, char *host, size_t host_len,
                   uint16_t *port, bool *use_tls);

/*
 * Fetch TURN credentials from server.
 * GET /api/turn/credentials
 */
int turn_fetch_credentials(const char *base_url, turn_creds_t *creds);

/*
 * Initialize TURN connection.
 */
int turn_init(turn_conn_t *conn, const char *server_url,
              const char *username, const char *credential);

/*
 * Allocate relay address on TURN server.
 * Performs unauthenticated request, receives 401 with nonce/realm,
 * then sends authenticated request.
 */
int turn_allocate(turn_conn_t *conn);

/*
 * Create permission for peer address.
 */
int turn_create_permission(turn_conn_t *conn, uint32_t peer_addr, uint16_t peer_port);

/*
 * Bind channel to peer for efficient data transfer.
 */
int turn_bind_channel(turn_conn_t *conn, uint32_t peer_addr, uint16_t peer_port);

/*
 * Send data via TURN channel.
 */
int turn_send(turn_conn_t *conn, const void *data, size_t len);

/*
 * Receive data via TURN channel (with timeout).
 * Returns number of bytes received, 0 on timeout, -1 on error.
 */
ssize_t turn_recv(turn_conn_t *conn, void *data, size_t max_len, int timeout_ms);

/*
 * Check if data is available (non-blocking).
 */
bool turn_poll_readable(turn_conn_t *conn, int timeout_ms);

/*
 * Refresh TURN allocation.
 */
int turn_refresh(turn_conn_t *conn);

/*
 * Close TURN connection.
 */
void turn_close(turn_conn_t *conn);

/*
 * Get error string.
 */
const char *turn_error_string(turn_error_t err);

/* ===================== STUN Message Building ===================== */

/*
 * Generate random transaction ID.
 */
void stun_generate_txn_id(uint8_t txn_id[12]);

/*
 * Build STUN message header.
 */
size_t stun_build_header(uint8_t *buf, uint16_t msg_type, const uint8_t txn_id[12]);

/*
 * Add string attribute.
 */
size_t stun_add_string_attr(uint8_t *buf, size_t offset, uint16_t type, const char *str);

/*
 * Add uint32 attribute.
 */
size_t stun_add_uint32_attr(uint8_t *buf, size_t offset, uint16_t type, uint32_t value);

/*
 * Add REQUESTED-TRANSPORT attribute.
 */
size_t stun_add_requested_transport(uint8_t *buf, size_t offset, uint8_t protocol);

/*
 * Add XOR-PEER-ADDRESS attribute.
 */
size_t stun_add_xor_peer_address(uint8_t *buf, size_t offset, uint32_t addr,
                                  uint16_t port, const uint8_t txn_id[12]);

/*
 * Add CHANNEL-NUMBER attribute.
 */
size_t stun_add_channel_number(uint8_t *buf, size_t offset, uint16_t channel);

/*
 * Compute and add MESSAGE-INTEGRITY attribute.
 */
size_t stun_add_message_integrity(uint8_t *buf, size_t len, const uint8_t key[16]);

/*
 * Update message length in header.
 */
void stun_update_length(uint8_t *buf, size_t len);

/*
 * Compute TURN long-term credential key.
 * key = MD5(username ":" realm ":" password)
 */
void turn_compute_key(const char *username, const char *realm,
                      const char *password, uint8_t key[16]);

/* ===================== STUN Message Parsing ===================== */

/*
 * Parse STUN message header.
 * Returns message type, or -1 on error.
 */
int stun_parse_header(const uint8_t *buf, size_t len, uint16_t *msg_len,
                      uint8_t txn_id[12]);

/*
 * Find attribute in message.
 * Returns pointer to attribute value, NULL if not found.
 */
const uint8_t *stun_find_attr(const uint8_t *msg, size_t msg_len,
                               uint16_t attr_type, uint16_t *attr_len);

/*
 * Parse XOR-RELAYED-ADDRESS or XOR-MAPPED-ADDRESS.
 */
int stun_parse_xor_address(const uint8_t *attr, uint16_t attr_len,
                           const uint8_t txn_id[12],
                           uint32_t *addr, uint16_t *port);

/*
 * Parse ERROR-CODE attribute.
 */
int stun_parse_error_code(const uint8_t *attr, uint16_t attr_len,
                          int *code, char *reason, size_t reason_len);

/*
 * Parse string attribute (REALM, NONCE, etc).
 */
int stun_parse_string_attr(const uint8_t *attr, uint16_t attr_len,
                           char *str, size_t str_len);

/* ===================== Packet Loss Test ===================== */

/*
 * Run packet loss test using TURN.
 * This is a simplified protocol that doesn't require full WebRTC/DTLS.
 * The server must support this simplified packet-test protocol.
 */
int turn_run_packet_loss_test(const char *base_url, packet_loss_result_t *result);

#endif /* NETSPEED_TURN_H */
