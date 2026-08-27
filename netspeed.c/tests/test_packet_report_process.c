#include "http.h"
#include "packet_loss.h"
#include "timing.h"

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int main(int argc, char **argv)
{
    if (argc != 3) {
        fprintf(stderr, "usage: %s SERVER_URL TOKEN\n", argv[0]);
        return 2;
    }
    volatile sig_atomic_t aborted = 0;
    if (http_global_init() != ERR_OK) {
        fprintf(stderr, "http initialization failed\n");
        return 1;
    }
    http_client_t http;
    if (http_client_init(&http, argv[1], argv[2], 10000, &aborted) != ERR_OK) {
        fprintf(stderr, "http client initialization failed\n");
        http_global_cleanup();
        return 1;
    }
    struct timespec deadline;
    timing_now(&deadline);
    deadline.tv_sec += 10;
    packet_loss_config_t config = {
        .http = &http,
        .server_frame_version = NETSPEED_PACKET_FRAME_VERSION,
        .deadline = deadline,
        .aborted = &aborted,
        .progress = NULL,
    };
    packet_loss_result_t result;
    char error[512] = {0};
    int status = packet_loss_run(&config, &result, error, sizeof(error));
    http_global_cleanup();
    if (status != ERR_OK) {
        fprintf(stderr, "packet_loss_run returned %d: %s\n", status, error);
        return 1;
    }
    if (!result.unavailable) {
        fprintf(stderr, "ok:false packet report was accepted as a measurement\n");
        return 1;
    }
    if (!strstr(result.reason, "server rejected packet report")) {
        fprintf(stderr, "unexpected unavailable reason: %s\n", result.reason);
        return 1;
    }
    puts("C packet-report rejection process test passed");
    return 0;
}
