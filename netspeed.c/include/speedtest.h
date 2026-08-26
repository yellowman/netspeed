/* speedtest.h - Protocol-v2 C measurement orchestration. */
#ifndef NETSPEED_SPEEDTEST_H
#define NETSPEED_SPEEDTEST_H

#include "http.h"
#include "types.h"

#include <pthread.h>
#include <signal.h>

typedef void (*progress_fn)(const char *stage, int current, int total, double value);

typedef struct speedtest {
    config_t *config;
    http_client_t http;
    results_t results;
    progress_fn on_progress;
    volatile sig_atomic_t aborted;
    struct timespec deadline;
    pthread_mutex_t error_mutex;
    char last_error[MAX_ERROR_LEN];
} speedtest_t;

void speedtest_init(speedtest_t *test, config_t *config);
void speedtest_set_progress(speedtest_t *test, progress_fn progress);
int speedtest_run(speedtest_t *test);
void speedtest_abort(speedtest_t *test);
void speedtest_cleanup(speedtest_t *test);
const results_t *speedtest_results(const speedtest_t *test);
const char *speedtest_error(const speedtest_t *test);

int speedtest_fetch_meta(speedtest_t *test);
int speedtest_latency(speedtest_t *test, const char *condition, int count);
int speedtest_download(speedtest_t *test);
int speedtest_upload(speedtest_t *test);
int speedtest_packet_loss(speedtest_t *test);

#endif
