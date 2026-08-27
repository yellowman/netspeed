/*
 * types.h - Shared protocol-v2 types for the native C netspeed client.
 */
#ifndef NETSPEED_TYPES_H
#define NETSPEED_TYPES_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <time.h>

#ifndef NETSPEED_VERSION
#define NETSPEED_VERSION "dev"
#endif
#ifndef NETSPEED_COMMIT
#define NETSPEED_COMMIT "unknown"
#endif
#ifndef NETSPEED_BUILD_DATE
#define NETSPEED_BUILD_DATE "unknown"
#endif

#define NETSPEED_MEASUREMENT_PROTOCOL_VERSION 2
#define NETSPEED_UPLOAD_RECEIPT_VERSION 1
#define NETSPEED_PACKET_FRAME_VERSION 1
#define NETSPEED_PACKET_FRAME_SIZE 1200

#define MAX_URL_LEN 2048
#define MAX_HOSTNAME_LEN 256
#define MAX_TOKEN_LEN 1024
#define MAX_SAMPLES 1024
#define MAX_WARNINGS 8
#define MAX_WARNING_LEN 192
#define MAX_ICE_SERVERS 16
#define MAX_ICE_SERVER_LEN 512
#define MAX_JSON_BODY (1024 * 1024)
#define MAX_ERROR_LEN 512

#define LATENCY_PROBES_FULL 20
#define LATENCY_PROBES_QUICK 5
#define INITIAL_PROBES 3
#define LATENCY_BATCH_SIZE 5
#define LOADED_PROBES_FULL 5
#define LOADED_PROBES_QUICK 3

#define BASELINE_SMALL_BYTES 100000LL
#define BASELINE_LARGE_BYTES 1000000LL
#define BASELINE_RUNS 3
#define MIN_WINDOW_CHUNK_BYTES 100000LL
#define MAX_WINDOW_CHUNK_BYTES (256LL * 1024LL * 1024LL)
#define WINDOW_TARGET_MS 250
#define WINDOW_DURATION_FULL_MS 1500
#define WINDOW_DURATION_QUICK_MS 1000
#define WINDOW_COUNT_FULL 3
#define WINDOW_COUNT_QUICK 1

#ifndef PACKET_LOSS_COUNT
#define PACKET_LOSS_COUNT 1000
#endif
#ifndef PACKET_LOSS_INTERVAL_MS
#define PACKET_LOSS_INTERVAL_MS 10
#endif
#ifndef PACKET_LOSS_DRAIN_MS
#define PACKET_LOSS_DRAIN_MS 3000
#endif

#define DEFAULT_SERVER_URL "http://localhost:8080"
#define DEFAULT_TEST_TIMEOUT_MS 60000LL

/* A single latency measurement. */
typedef struct {
    int64_t ts;
    int64_t started_at;
    int64_t ended_at;
    double rtt_ms;
    char condition[16];
    bool load_overlapped;
    bool load_tracking_accurate;
    char timing_source[48];
} latency_sample_t;

/* A single baseline request or fixed-duration aggregate window. */
typedef struct {
    int64_t ts;
    char direction[16];
    int64_t size_bytes;
    double duration_ms;
    double mbps;
    char profile[24];
    int run_index;
    char sample_kind[16];
    int window_index;
    bool has_window_index;
    int concurrency;
    int64_t chunk_bytes;
    int request_count;
    char timing_source[48];
} throughput_sample_t;

typedef struct {
    double min;
    double median;
    double p90;
} rtt_stats_t;

typedef struct {
    int sent;
    int received;
    double loss_percent;
    double transaction_loss_percent;
    int forward_sent;
    int forward_received;
    double forward_loss_percent;
    bool forward_loss_available;
    int acknowledgements_sent;
    int acknowledgements_received;
    double reverse_acknowledgement_loss_percent;
    bool reverse_loss_available;
    int frame_size_bytes;
    int duplicate_frames;
    int invalid_frames;
    int ack_send_failures;
    rtt_stats_t rtt_stats_ms;
    double jitter_ms;
    char test_id[96];
    bool unavailable;
    char reason[256];
} packet_loss_result_t;

typedef struct {
    double download_mbps;
    double upload_mbps;
    double latency_unloaded_ms;
    double latency_download_ms;
    double latency_upload_ms;
    double jitter_ms;
    double packet_loss_percent;
    bool packet_loss_available;
} summary_t;

typedef struct {
    char video_streaming[16];
    char gaming[16];
    char video_chatting[16];
} quality_t;

typedef struct {
    int download_windows;
    int upload_windows;
    int unloaded_latency;
    int download_loaded_latency;
    int upload_loaded_latency;
    bool adequate;
} confidence_sample_count_t;

typedef struct {
    double download;
    double upload;
    double latency;
    bool acceptable;
} confidence_variability_t;

typedef struct {
    int download_accepted;
    int upload_accepted;
    bool complete;
} confidence_overlap_t;

typedef struct {
    char overall[12];
    int overall_score;
    confidence_sample_count_t sample_count;
    confidence_variability_t variability;
    confidence_overlap_t loaded_overlap;
    bool timing_accurate;
    bool packet_test_completed;
    char warnings[MAX_WARNINGS][MAX_WARNING_LEN];
    int warning_count;
} test_confidence_t;

typedef struct {
    char hostname[MAX_HOSTNAME_LEN];
    char client_ip[64];
    char http_protocol[16];
    int asn;
    char as_organization[128];
    char colo[16];
    char country[8];
    char city[96];
    char region[96];
    char postal_code[24];
    double latitude;
    double longitude;
    char timezone[64];
    int64_t max_transfer_bytes;
    int max_concurrent_transfers_per_client;
    int measurement_protocol_version;
    int upload_receipt_version;
    int packet_loss_frame_version;
} meta_t;

typedef struct {
    meta_t meta;
    summary_t summary;
    quality_t quality;
    test_confidence_t test_confidence;
    throughput_sample_t throughput_samples[MAX_SAMPLES];
    int throughput_count;
    latency_sample_t latency_samples[MAX_SAMPLES];
    int latency_count;
    packet_loss_result_t packet_loss;
    bool packet_loss_present;
    int64_t timestamp;
    int64_t start_time;
    int64_t end_time;
    char start_time_rfc3339[48];
    char end_time_rfc3339[48];
    char server_url[MAX_URL_LEN];
} results_t;

typedef struct {
    char server_url[MAX_URL_LEN];
    char access_token[MAX_TOKEN_LEN];
    bool json_output;
    bool csv_output;
    bool quiet;
    bool verbose;
    bool quick;
    bool download_only;
    bool upload_only;
    bool skip_packet_loss;
    bool no_color;
    int64_t timeout_ms;
} config_t;

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
    ERR_PROTOCOL = 9,
    ERR_INCOMPLETE = 10
} ns_error_t;

#endif /* NETSPEED_TYPES_H */
