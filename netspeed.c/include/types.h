/*
 * types.h - Common types and constants for netspeed
 */

#ifndef NETSPEED_TYPES_H
#define NETSPEED_TYPES_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include <time.h>

/* Version */
#define NETSPEED_VERSION "1.0.0"

/* Limits */
#define MAX_URL_LEN         2048
#define MAX_HOSTNAME_LEN    256
#define MAX_SAMPLES         256
#define MAX_PROFILES        20   /* Up to 125GB profiles */

/* Buffer sizes for high-speed transfers */
#define READ_BUFFER_SIZE    (4 * 1024 * 1024)  /* 4MB */
#define WRITE_BUFFER_SIZE   (4 * 1024 * 1024)  /* 4MB */

/* Time budget constants (matching Go/JS clients) */
#define MAX_TEST_DURATION_MS      4000   /* 4 seconds per profile */
#define TOTAL_PHASE_BUDGET_MS     8000   /* 8 seconds per phase */
#define LOW_LATENCY_THRESHOLD_MS  50
#define HIGH_LATENCY_THRESHOLD_MS 100
#define MIN_BANDWIDTH_FOR_PARALLEL 2.0   /* Mbps */

/* Latency probe counts */
#define LATENCY_PROBES_FULL  20
#define LATENCY_PROBES_QUICK 5
#define INITIAL_PROBES       3
#define BATCH_SIZE           5

/* Packet loss test */
#define PACKET_LOSS_COUNT    1000
#define PACKET_LOSS_INTERVAL_MS 10

/* Timeouts */
#define CONNECT_TIMEOUT_SEC  30
#define REQUEST_TIMEOUT_SEC  120

/* Profile definition */
typedef struct {
    const char *name;
    int64_t bytes;
    int runs;
} profile_t;

/* Timing info from precise measurement */
typedef struct {
    struct timespec wrote_request;
    struct timespec got_first_byte;
    struct timespec body_done;
    bool wrote_request_set;
    bool got_first_byte_set;
} timing_info_t;

/* Latency sample */
typedef struct {
    int64_t ts;           /* Unix timestamp ms */
    double rtt_ms;
    const char *phase;    /* "unloaded", "download", "upload" */
} latency_sample_t;

/* Throughput sample */
typedef struct {
    int64_t ts;           /* Unix timestamp ms */
    const char *direction; /* "download", "upload" */
    int64_t size_bytes;
    double duration_ms;
    double mbps;
    const char *profile;
    int run_index;
} throughput_sample_t;

/* RTT statistics */
typedef struct {
    double min;
    double median;
    double p90;
} rtt_stats_t;

/* Packet loss result */
typedef struct {
    int sent;
    int received;
    double loss_percent;
    rtt_stats_t rtt_stats_ms;
    double jitter_ms;
    char test_id[64];
    bool unavailable;
    char reason[256];
} packet_loss_result_t;

/* Summary statistics */
typedef struct {
    double download_mbps;
    double upload_mbps;
    double latency_unloaded_ms;
    double latency_download_ms;
    double latency_upload_ms;
    double jitter_ms;
    double packet_loss_percent;
} summary_t;

/* Network quality grades */
typedef struct {
    char video_streaming[16];
    char gaming[16];
    char video_chatting[16];
} quality_t;

/* Server/client metadata */
typedef struct {
    char hostname[MAX_HOSTNAME_LEN];
    char client_ip[64];
    char http_protocol[16];
    int asn;
    char as_organization[128];
    char colo[8];
    char country[4];
    char city[64];
    char region[64];
    char postal_code[16];
    double latitude;
    double longitude;
    char timezone[64];
} meta_t;

/* Test results */
typedef struct {
    meta_t meta;
    summary_t summary;
    quality_t quality;

    throughput_sample_t throughput_samples[MAX_SAMPLES];
    int throughput_count;

    latency_sample_t latency_samples[MAX_SAMPLES];
    int latency_count;

    packet_loss_result_t packet_loss;

    int64_t start_time;  /* Unix timestamp ms */
    int64_t end_time;    /* Unix timestamp ms */
} results_t;

/* CLI configuration */
typedef struct {
    char server_url[MAX_URL_LEN];
    bool json_output;
    bool csv_output;
    bool quiet;
    bool verbose;
    bool quick;
    bool download_only;
    bool upload_only;
    bool skip_packet_loss;
    bool no_color;
    int timeout_sec;
} config_t;

/* Error codes */
typedef enum {
    ERR_OK = 0,
    ERR_NETWORK = 1,
    ERR_TIMEOUT = 2,
    ERR_DNS = 3,
    ERR_TLS = 4,
    ERR_HTTP = 5,
    ERR_PARSE = 6,
    ERR_MEMORY = 7,
    ERR_ARGS = 8,
} ns_error_t;

#endif /* NETSPEED_TYPES_H */
