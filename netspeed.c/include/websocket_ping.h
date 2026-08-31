/* websocket_ping.h - Persistent libcurl connected-socket latency echo. */
#ifndef NETSPEED_WEBSOCKET_PING_H
#define NETSPEED_WEBSOCKET_PING_H

#include "http.h"

#include <stdbool.h>
#include <stdint.h>

#define NETSPEED_WEBSOCKET_PING_PROTOCOL "netspeed.ping.v1"
#define NETSPEED_WEBSOCKET_PING_PAYLOAD_BYTES 16

typedef struct {
    const http_client_t *client;
    CURL *easy;
    bool connected;
    char endpoint_path[MAX_MEASUREMENT_PATH];
    char error[MAX_ERROR_LEN];
} websocket_ping_session_t;

bool websocket_ping_supported(void);
void websocket_ping_session_init(websocket_ping_session_t *session,
                                 const http_client_t *client);
void websocket_ping_session_cleanup(websocket_ping_session_t *session);
const char *websocket_ping_error(const websocket_ping_session_t *session);

int websocket_ping_open(websocket_ping_session_t *session,
                        const char *endpoint_path,
                        const char *protocol,
                        int payload_bytes);
int websocket_ping_measure(websocket_ping_session_t *session,
                           uint32_t sequence,
                           double *rtt_ms);

#endif /* NETSPEED_WEBSOCKET_PING_H */
