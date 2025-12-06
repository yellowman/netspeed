/*
 * speedtest.h - Speed test orchestration
 *
 * Coordinates all test phases: latency, download, upload, packet loss.
 */

#ifndef NETSPEED_SPEEDTEST_H
#define NETSPEED_SPEEDTEST_H

#include "types.h"
#include "http.h"

/* Progress callback */
typedef void (*progress_fn)(const char *phase, int current, int total, double value);

/* Speed test context */
typedef struct {
    config_t *config;
    http_conn_t conn;
    results_t results;
    progress_fn on_progress;
    bool aborted;
} speedtest_t;

/*
 * Initialize speed test context.
 */
void speedtest_init(speedtest_t *st, config_t *config);

/*
 * Set progress callback.
 */
void speedtest_set_progress(speedtest_t *st, progress_fn fn);

/*
 * Run full speed test suite.
 * Returns 0 on success, error code on failure.
 */
int speedtest_run(speedtest_t *st);

/*
 * Abort running test.
 */
void speedtest_abort(speedtest_t *st);

/*
 * Cleanup speed test context.
 */
void speedtest_cleanup(speedtest_t *st);

/*
 * Get results (valid after speedtest_run completes).
 */
const results_t *speedtest_results(const speedtest_t *st);

/* ===================== Individual Test Phases ===================== */

/*
 * Fetch server metadata.
 * Returns 0 on success.
 */
int speedtest_fetch_meta(speedtest_t *st);

/*
 * Run latency test with adaptive batching.
 * phase: "unloaded", "download", or "upload"
 * count: number of probes
 * Returns 0 on success.
 */
int speedtest_latency(speedtest_t *st, const char *phase, int count);

/*
 * Run download speed test.
 * Returns 0 on success.
 */
int speedtest_download(speedtest_t *st);

/*
 * Run upload speed test.
 * Returns 0 on success.
 */
int speedtest_upload(speedtest_t *st);

/*
 * Run packet loss test (stub - WebRTC not implemented).
 * Returns 0, sets unavailable flag.
 */
int speedtest_packet_loss(speedtest_t *st);

/* ===================== Measurement Helpers ===================== */

/*
 * Measure single latency probe.
 * RTT = GotFirstResponseByte - WroteRequest
 * Returns RTT in milliseconds, or -1 on error.
 */
double measure_latency(http_conn_t *conn, const char *base_url,
                       const char *phase, int seq);

/*
 * Measure single download.
 * Duration = BodyDone - GotFirstResponseByte
 * Returns Mbps, or -1 on error.
 */
double measure_download(http_conn_t *conn, const char *base_url,
                        const char *profile, int64_t bytes, int run,
                        throughput_sample_t *sample);

/*
 * Measure single upload.
 * Duration = GotFirstResponseByte - BodyWriteStart
 * Returns Mbps, or -1 on error.
 */
double measure_upload(http_conn_t *conn, const char *base_url,
                      const char *profile, const void *payload,
                      size_t payload_len, int run,
                      throughput_sample_t *sample);

/*
 * Quick bandwidth estimate for adaptive decisions.
 * Downloads 100KB and returns Mbps.
 */
double quick_bandwidth_estimate(http_conn_t *conn, const char *base_url);

/*
 * Select profiles based on estimated speed.
 * Returns number of selected profiles.
 */
int select_profiles(double estimated_speed, const profile_t *all_profiles,
                    int all_count, const profile_t *baseline,
                    int baseline_count, profile_t *selected);

/*
 * Estimate transfer time in milliseconds.
 */
double estimate_transfer_time_ms(int64_t bytes, double speed_mbps);

#endif /* NETSPEED_SPEEDTEST_H */
