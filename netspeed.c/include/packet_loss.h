/* packet_loss.h - Exact 1,200-byte WebRTC packet-loss test. */
#ifndef NETSPEED_PACKET_LOSS_H
#define NETSPEED_PACKET_LOSS_H

#include "http.h"
#include "types.h"

#include <signal.h>

typedef void (*packet_progress_fn)(const char *stage, int current, int total, double value);

typedef struct {
    http_client_t *http;
    int server_frame_version;
    struct timespec deadline;
    volatile sig_atomic_t *aborted;
    packet_progress_fn progress;
} packet_loss_config_t;

int packet_loss_run(const packet_loss_config_t *config, packet_loss_result_t *result,
                    char *error, size_t error_len);

/* Pure protocol helpers are public for unit tests. */
void packet_frame_encode_probe(uint8_t frame[NETSPEED_PACKET_FRAME_SIZE],
                               uint32_t sequence, int64_t sent_at_ms);
int packet_frame_decode(const uint8_t frame[NETSPEED_PACKET_FRAME_SIZE], size_t size,
                        bool *acknowledgement, uint32_t *sequence,
                        int64_t *sent_at_ms, int64_t *received_at_ms);

#endif
