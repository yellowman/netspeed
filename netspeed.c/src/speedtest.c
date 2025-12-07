/*
 * speedtest.c - Speed test orchestration
 *
 * Coordinates all test phases: latency, download, upload, packet loss.
 * Implements precise timing methodology matching Go/JS clients.
 */

#include "speedtest.h"
#include "json.h"
#include "stats.h"
#include "timing.h"
#include "turn.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

/* All download profiles matching web client (up to 1 Tbps)
 * Sizes use decimal notation: 1 kB = 1000 bytes, 1 MB = 1,000,000 bytes */
static const profile_t download_profiles[] = {
    {"100kB",  100000LL,              10},  /* baseline */
    {"1MB",    1000000LL,             8},   /* baseline */
    {"10MB",   10000000LL,            6},
    {"25MB",   25000000LL,            4},
    {"100MB",  100000000LL,           3},
    {"250MB",  250000000LL,           2},
    {"500MB",  500000000LL,           2},   /* 1s at 4 Gbps */
    {"1GB",    1000000000LL,          2},   /* 1s at 8 Gbps */
    {"2GB",    2000000000LL,          2},   /* 1s at 16 Gbps */
    {"5GB",    5000000000LL,          2},   /* 1s at 40 Gbps */
    {"12GB",   12000000000LL,         2},   /* 1s at ~100 Gbps */
    {"50GB",   50000000000LL,         2},   /* 1s at 400 Gbps */
    {"100GB",  100000000000LL,        2},   /* 1s at 800 Gbps */
    {"125GB",  125000000000LL,        2},   /* 1s at 1 Tbps */
};
#define NUM_DOWNLOAD_PROFILES (sizeof(download_profiles) / sizeof(download_profiles[0]))

/* All upload profiles matching web client (up to 1 Tbps) */
static const profile_t upload_profiles[] = {
    {"100kB",  100000LL,              8},   /* baseline */
    {"1MB",    1000000LL,             6},   /* baseline */
    {"10MB",   10000000LL,            4},
    {"25MB",   25000000LL,            4},
    {"50MB",   50000000LL,            3},
    {"100MB",  100000000LL,           2},
    {"250MB",  250000000LL,           2},   /* 1s at 2 Gbps */
    {"500MB",  500000000LL,           2},   /* 1s at 4 Gbps */
    {"1GB",    1000000000LL,          2},   /* 1s at 8 Gbps */
    {"2GB",    2000000000LL,          2},   /* 1s at 16 Gbps */
    {"5GB",    5000000000LL,          2},   /* 1s at 40 Gbps */
    {"12GB",   12000000000LL,         2},   /* 1s at ~100 Gbps */
    {"50GB",   50000000000LL,         2},   /* 1s at 400 Gbps */
    {"100GB",  100000000000LL,        2},   /* 1s at 800 Gbps */
    {"125GB",  125000000000LL,        2},   /* 1s at 1 Tbps */
};
#define NUM_UPLOAD_PROFILES (sizeof(upload_profiles) / sizeof(upload_profiles[0]))

/* Baseline profiles for quick test */
static const profile_t baseline_download[] = {
    {"100kB",  100000LL,              10},
    {"1MB",    1000000LL,             8},
};
#define NUM_BASELINE_DOWNLOAD (sizeof(baseline_download) / sizeof(baseline_download[0]))

static const profile_t baseline_upload[] = {
    {"100kB",  100000LL,              8},
    {"1MB",    1000000LL,             6},
};
#define NUM_BASELINE_UPLOAD (sizeof(baseline_upload) / sizeof(baseline_upload[0]))

/* Static upload payload buffer */
static char *upload_payload = NULL;
static size_t upload_payload_size = 0;

static char *get_upload_payload(size_t size)
{
    if (upload_payload_size < size) {
        free(upload_payload);
        upload_payload = malloc(size);
        if (upload_payload) {
            /* Fill with random-ish data */
            for (size_t i = 0; i < size; i++) {
                upload_payload[i] = (char)(i & 0xFF);
            }
            upload_payload_size = size;
        }
    }
    return upload_payload;
}

void speedtest_init(speedtest_t *st, config_t *config)
{
    memset(st, 0, sizeof(*st));
    st->config = config;
    http_conn_init(&st->conn);
    st->aborted = false;
}

void speedtest_set_progress(speedtest_t *st, progress_fn fn)
{
    st->on_progress = fn;
}

void speedtest_cleanup(speedtest_t *st)
{
    http_disconnect(&st->conn);
}

const results_t *speedtest_results(const speedtest_t *st)
{
    return &st->results;
}

void speedtest_abort(speedtest_t *st)
{
    st->aborted = true;
}

static void report_progress(speedtest_t *st, const char *phase,
                           int current, int total, double value)
{
    if (st->on_progress) {
        st->on_progress(phase, current, total, value);
    }
}

/* ===================== Measurement Functions ===================== */

double measure_latency(http_conn_t *conn, const char *base_url,
                       const char *phase, int seq)
{
    (void)base_url; /* Already connected */
    (void)phase;
    (void)seq;

    /* Send request for smallest payload */
    char path[256];
    snprintf(path, sizeof(path), "/__down?bytes=0");

    http_response_t resp;
    int err = http_get(conn, path, &resp);

    if (err != ERR_OK) {
        return -1;
    }

    /* RTT = GotFirstResponseByte - WroteRequest */
    double rtt_ms = timing_diff_ms(&resp.timing.wrote_request,
                                   &resp.timing.got_first_byte);

    http_response_free(&resp);
    return rtt_ms;
}

double measure_download(http_conn_t *conn, const char *base_url,
                        const char *profile, int64_t bytes, int run,
                        throughput_sample_t *sample)
{
    (void)base_url;
    (void)profile;
    (void)run;

    char path[256];
    snprintf(path, sizeof(path), "/__down?bytes=%lld", (long long)bytes);

    http_response_t resp;
    int err = http_get(conn, path, &resp);

    if (err != ERR_OK) {
        return -1;
    }

    /* Duration = BodyDone - GotFirstResponseByte */
    double duration_ms = timing_diff_ms(&resp.timing.got_first_byte,
                                        &resp.timing.body_done);

    if (duration_ms <= 0) {
        http_response_free(&resp);
        return -1;
    }

    double mbps = bytes_to_mbps(bytes, duration_ms);

    if (sample) {
        sample->ts = timing_now_ms();
        sample->direction = "download";
        sample->size_bytes = bytes;
        sample->duration_ms = duration_ms;
        sample->mbps = mbps;
        sample->profile = profile;
        sample->run_index = run;
    }

    http_response_free(&resp);
    return mbps;
}

double measure_upload(http_conn_t *conn, const char *base_url,
                      const char *profile, const void *payload,
                      size_t payload_len, int run,
                      throughput_sample_t *sample)
{
    (void)base_url;
    (void)profile;
    (void)run;

    http_response_t resp;
    int err = http_post(conn, "/__up", payload, payload_len, &resp);

    if (err != ERR_OK) {
        return -1;
    }

    /* Duration = GotFirstResponseByte - WroteRequest */
    double duration_ms = timing_diff_ms(&resp.timing.wrote_request,
                                        &resp.timing.got_first_byte);

    if (duration_ms <= 0) {
        http_response_free(&resp);
        return -1;
    }

    double mbps = bytes_to_mbps(payload_len, duration_ms);

    if (sample) {
        sample->ts = timing_now_ms();
        sample->direction = "upload";
        sample->size_bytes = payload_len;
        sample->duration_ms = duration_ms;
        sample->mbps = mbps;
        sample->profile = profile;
        sample->run_index = run;
    }

    http_response_free(&resp);
    return mbps;
}

double quick_bandwidth_estimate(http_conn_t *conn, const char *base_url)
{
    throughput_sample_t sample;
    return measure_download(conn, base_url, "100kB", 100000LL, 0, &sample);
}

double estimate_transfer_time_ms(int64_t bytes, double speed_mbps)
{
    if (speed_mbps <= 0) return MAX_TEST_DURATION_MS;

    /* bytes / (mbps * 1e6 / 8) * 1000 = bytes * 8 / (mbps * 1e3) */
    return (double)bytes * 8.0 / (speed_mbps * 1000.0);
}

/* ===================== Test Phases ===================== */

int speedtest_fetch_meta(speedtest_t *st)
{
    http_response_t resp;
    int err = http_get(&st->conn, "/meta", &resp);

    if (err != ERR_OK) {
        return err;
    }

    if (resp.status_code != 200) {
        http_response_free(&resp);
        return ERR_HTTP;
    }

    if (resp.body) {
        meta_from_json(resp.body, &st->results.meta);
    }

    http_response_free(&resp);
    return ERR_OK;
}

int speedtest_latency(speedtest_t *st, const char *phase, int count)
{
    double rtts[MAX_SAMPLES];
    int valid_count = 0;

    for (int i = 0; i < count && !st->aborted; i++) {
        double rtt = measure_latency(&st->conn, st->config->server_url, phase, i);

        if (rtt > 0) {
            rtts[valid_count] = rtt;

            /* Record sample */
            if (st->results.latency_count < MAX_SAMPLES) {
                latency_sample_t *sample = &st->results.latency_samples[st->results.latency_count++];
                sample->ts = timing_now_ms();
                sample->rtt_ms = rtt;
                sample->phase = phase;
            }

            valid_count++;
            report_progress(st, "latency", i + 1, count, rtt);
        }
    }

    if (valid_count == 0) {
        return ERR_NETWORK;
    }

    /* Sort array for statistics (stats_median expects sorted array) */
    stats_sort(rtts, valid_count);

    /* Compute statistics */
    double median = stats_median(rtts, valid_count);

    if (strcmp(phase, "unloaded") == 0) {
        st->results.summary.latency_unloaded_ms = median;
        st->results.summary.jitter_ms = stats_iqr(rtts, valid_count);
    } else if (strcmp(phase, "download") == 0) {
        st->results.summary.latency_download_ms = median;
    } else if (strcmp(phase, "upload") == 0) {
        st->results.summary.latency_upload_ms = median;
    }

    return ERR_OK;
}

int speedtest_download(speedtest_t *st)
{
    /* Quick bandwidth estimate */
    double estimated_speed = quick_bandwidth_estimate(&st->conn, st->config->server_url);
    if (estimated_speed <= 0) {
        estimated_speed = 10.0; /* Default assumption */
    }

    /* Select profiles based on speed estimate */
    const profile_t *profiles;
    int num_profiles;

    if (st->config->quick) {
        profiles = baseline_download;
        num_profiles = NUM_BASELINE_DOWNLOAD;
    } else {
        profiles = download_profiles;
        num_profiles = NUM_DOWNLOAD_PROFILES;
    }

    double all_speeds[MAX_SAMPLES];
    int speed_count = 0;
    int total_runs = 0;

    /* Count total runs for progress */
    for (int i = 0; i < num_profiles; i++) {
        total_runs += profiles[i].runs;
    }

    int current_run = 0;
    int64_t phase_start = timing_now_ms();

    for (int p = 0; p < num_profiles && !st->aborted; p++) {
        const profile_t *prof = &profiles[p];

        /* Check if transfer would exceed time budget */
        double est_time = estimate_transfer_time_ms(prof->bytes, estimated_speed);
        if (est_time > MAX_TEST_DURATION_MS * 2) {
            /* Skip profiles that would take too long */
            current_run += prof->runs;
            continue;
        }

        for (int r = 0; r < prof->runs && !st->aborted; r++) {
            /* Check phase time budget */
            if (timing_now_ms() - phase_start > TOTAL_PHASE_BUDGET_MS) {
                break;
            }

            throughput_sample_t sample;
            double mbps = measure_download(&st->conn, st->config->server_url,
                                          prof->name, prof->bytes, r, &sample);

            if (mbps > 0) {
                /* Record sample */
                if (st->results.throughput_count < MAX_SAMPLES) {
                    st->results.throughput_samples[st->results.throughput_count++] = sample;
                }

                all_speeds[speed_count++] = mbps;

                /* Update speed estimate for profile selection */
                if (speed_count >= 3) {
                    estimated_speed = stats_p90(all_speeds, speed_count);
                }

                report_progress(st, "download", ++current_run, total_runs, mbps);
            } else {
                current_run++;
            }
        }
    }

    if (speed_count == 0) {
        return ERR_NETWORK;
    }

    /* Use p90 as the final result (like Go client) */
    st->results.summary.download_mbps = stats_p90(all_speeds, speed_count);

    return ERR_OK;
}

int speedtest_upload(speedtest_t *st)
{
    /* Use download result for profile selection */
    double estimated_speed = st->results.summary.download_mbps;
    if (estimated_speed <= 0) {
        estimated_speed = 10.0;
    }

    /* Select profiles */
    const profile_t *profiles;
    int num_profiles;

    if (st->config->quick) {
        profiles = baseline_upload;
        num_profiles = NUM_BASELINE_UPLOAD;
    } else {
        profiles = upload_profiles;
        num_profiles = NUM_UPLOAD_PROFILES;
    }

    double all_speeds[MAX_SAMPLES];
    int speed_count = 0;
    int total_runs = 0;

    for (int i = 0; i < num_profiles; i++) {
        total_runs += profiles[i].runs;
    }

    int current_run = 0;
    int64_t phase_start = timing_now_ms();

    for (int p = 0; p < num_profiles && !st->aborted; p++) {
        const profile_t *prof = &profiles[p];

        /* Get upload payload */
        char *payload = get_upload_payload(prof->bytes);
        if (!payload) {
            continue;
        }

        /* Check if transfer would exceed time budget */
        double est_time = estimate_transfer_time_ms(prof->bytes, estimated_speed);
        if (est_time > MAX_TEST_DURATION_MS * 2) {
            current_run += prof->runs;
            continue;
        }

        for (int r = 0; r < prof->runs && !st->aborted; r++) {
            if (timing_now_ms() - phase_start > TOTAL_PHASE_BUDGET_MS) {
                break;
            }

            throughput_sample_t sample;
            double mbps = measure_upload(&st->conn, st->config->server_url,
                                        prof->name, payload, prof->bytes, r, &sample);

            if (mbps > 0) {
                if (st->results.throughput_count < MAX_SAMPLES) {
                    st->results.throughput_samples[st->results.throughput_count++] = sample;
                }

                all_speeds[speed_count++] = mbps;

                if (speed_count >= 3) {
                    estimated_speed = stats_p90(all_speeds, speed_count);
                }

                report_progress(st, "upload", ++current_run, total_runs, mbps);
            } else {
                current_run++;
            }
        }
    }

    if (speed_count == 0) {
        return ERR_NETWORK;
    }

    st->results.summary.upload_mbps = stats_p90(all_speeds, speed_count);

    return ERR_OK;
}

int speedtest_packet_loss(speedtest_t *st)
{
    /* Run TURN-based packet loss test */
    int err = turn_run_packet_loss_test(st->config->server_url, &st->results.packet_loss);
    if (err != TURN_OK) {
        st->results.packet_loss.unavailable = true;
        snprintf(st->results.packet_loss.reason, sizeof(st->results.packet_loss.reason),
                 "TURN error: %s", turn_error_string(err));
    }
    return ERR_OK;
}

/* ===================== Quality Grading ===================== */

static const char *grade_speed(double mbps, double thresholds[4])
{
    if (mbps >= thresholds[0]) return "Great";
    if (mbps >= thresholds[1]) return "Good";
    if (mbps >= thresholds[2]) return "Okay";
    return "Poor";
}

static const char *grade_latency(double ms, double thresholds[4])
{
    if (ms <= thresholds[0]) return "Great";
    if (ms <= thresholds[1]) return "Good";
    if (ms <= thresholds[2]) return "Okay";
    return "Poor";
}

static void compute_quality(results_t *r)
{
    /* Video Streaming thresholds */
    double video_dl[] = {25, 10, 5, 0};
    strncpy(r->quality.video_streaming,
            grade_speed(r->summary.download_mbps, video_dl),
            sizeof(r->quality.video_streaming) - 1);

    /* Gaming thresholds (latency-focused) */
    double gaming_lat[] = {30, 50, 100, 999999};
    strncpy(r->quality.gaming,
            grade_latency(r->summary.latency_unloaded_ms, gaming_lat),
            sizeof(r->quality.gaming) - 1);

    /* Video Chatting (balanced) */
    double chat_up[] = {5, 2, 1, 0};
    double chat_lat[] = {100, 200, 400, 999999};

    const char *up_grade = grade_speed(r->summary.upload_mbps, chat_up);
    const char *lat_grade = grade_latency(r->summary.latency_unloaded_ms, chat_lat);

    /* Use worse of upload or latency */
    int up_score = 0, lat_score = 0;
    if (strcmp(up_grade, "Great") == 0) up_score = 3;
    else if (strcmp(up_grade, "Good") == 0) up_score = 2;
    else if (strcmp(up_grade, "Okay") == 0) up_score = 1;

    if (strcmp(lat_grade, "Great") == 0) lat_score = 3;
    else if (strcmp(lat_grade, "Good") == 0) lat_score = 2;
    else if (strcmp(lat_grade, "Okay") == 0) lat_score = 1;

    int final_score = up_score < lat_score ? up_score : lat_score;
    const char *grades[] = {"Poor", "Okay", "Good", "Great"};
    strncpy(r->quality.video_chatting, grades[final_score],
            sizeof(r->quality.video_chatting) - 1);
}

/* ===================== Main Run Function ===================== */

int speedtest_run(speedtest_t *st)
{
    int err;
    url_t url;

    /* Parse server URL */
    if (url_parse(st->config->server_url, &url) < 0) {
        return ERR_ARGS;
    }

    /* Record start time */
    st->results.start_time = timing_now_ms();

    /* Connect to server */
    err = http_connect(&st->conn, url.host, url.port,
                       strcmp(url.scheme, "https") == 0);
    if (err != ERR_OK) {
        return err;
    }

    /* Fetch metadata */
    err = speedtest_fetch_meta(st);
    if (err != ERR_OK) {
        /* Non-fatal: continue without metadata */
    }

    /* Run unloaded latency test */
    if (!st->aborted) {
        int probes = st->config->quick ? LATENCY_PROBES_QUICK : LATENCY_PROBES_FULL;
        speedtest_latency(st, "unloaded", probes);
    }

    /* Run download test */
    if (!st->aborted && !st->config->upload_only) {
        err = speedtest_download(st);
        if (err != ERR_OK && !st->config->download_only) {
            /* Continue to upload even if download fails */
        }

        /* Download latency probes */
        if (!st->aborted) {
            speedtest_latency(st, "download", LATENCY_PROBES_QUICK);
        }
    }

    /* Run upload test */
    if (!st->aborted && !st->config->download_only) {
        err = speedtest_upload(st);

        /* Upload latency probes */
        if (!st->aborted) {
            speedtest_latency(st, "upload", LATENCY_PROBES_QUICK);
        }
    }

    /* Run packet loss test */
    if (!st->aborted && !st->config->skip_packet_loss) {
        speedtest_packet_loss(st);
    }

    /* Record end time */
    st->results.end_time = timing_now_ms();

    /* Compute quality grades */
    compute_quality(&st->results);

    return ERR_OK;
}
