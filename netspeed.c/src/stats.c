/*
 * stats.c - Statistical functions
 */

#include "stats.h"
#include <stdlib.h>
#include <string.h>
#include <math.h>

static int cmp_double(const void *a, const void *b)
{
    double da = *(const double *)a;
    double db = *(const double *)b;
    if (da < db) return -1;
    if (da > db) return 1;
    return 0;
}

void stats_sort(double *arr, int n)
{
    if (n > 1) {
        qsort(arr, n, sizeof(double), cmp_double);
    }
}

double stats_percentile(const double *sorted, int n, double p)
{
    if (n == 0) return 0.0;
    if (n == 1) return sorted[0];

    double idx = (n - 1) * p / 100.0;
    int lo = (int)idx;
    int hi = lo + 1;
    if (hi >= n) hi = n - 1;

    double frac = idx - lo;
    return sorted[lo] * (1.0 - frac) + sorted[hi] * frac;
}

double stats_median(const double *sorted, int n)
{
    return stats_percentile(sorted, n, 50.0);
}

double stats_min(const double *arr, int n)
{
    if (n == 0) return 0.0;
    double m = arr[0];
    for (int i = 1; i < n; i++) {
        if (arr[i] < m) m = arr[i];
    }
    return m;
}

double stats_max(const double *arr, int n)
{
    if (n == 0) return 0.0;
    double m = arr[0];
    for (int i = 1; i < n; i++) {
        if (arr[i] > m) m = arr[i];
    }
    return m;
}

double stats_mean(const double *arr, int n)
{
    if (n == 0) return 0.0;
    double sum = 0.0;
    for (int i = 0; i < n; i++) {
        sum += arr[i];
    }
    return sum / n;
}

double stats_jitter(const double *sorted, int n)
{
    double p90 = stats_percentile(sorted, n, 90.0);
    double p50 = stats_percentile(sorted, n, 50.0);
    return p90 - p50;
}

double stats_iqr(double *arr, int n)
{
    if (n == 0) return 0.0;
    stats_sort(arr, n);
    double p75 = stats_percentile(arr, n, 75.0);
    double p25 = stats_percentile(arr, n, 25.0);
    return p75 - p25;
}

double stats_p90(double *arr, int n)
{
    if (n == 0) return 0.0;
    stats_sort(arr, n);
    return stats_percentile(arr, n, 90.0);
}

double bytes_to_mbps(int64_t bytes, double duration_ms)
{
    if (duration_ms <= 0) return 0.0;
    /* (bytes * 8) / (duration_ms / 1000) / 1,000,000 = bytes * 8 / duration_ms / 1000 */
    return (double)bytes * 8.0 / duration_ms / 1000.0;
}

void calculate_summary(results_t *r)
{
    summary_t *s = &r->summary;
    memset(s, 0, sizeof(*s));

    /* Collect download speeds */
    double dl_speeds[MAX_SAMPLES];
    int dl_count = 0;
    for (int i = 0; i < r->throughput_count; i++) {
        if (strcmp(r->throughput_samples[i].direction, "download") == 0) {
            /* Filter samples with duration >= 10ms */
            if (r->throughput_samples[i].duration_ms >= 10.0) {
                dl_speeds[dl_count++] = r->throughput_samples[i].mbps;
            }
        }
    }

    if (dl_count > 0) {
        stats_sort(dl_speeds, dl_count);
        s->download_mbps = stats_percentile(dl_speeds, dl_count, 90.0);
    }

    /* Collect upload speeds */
    double ul_speeds[MAX_SAMPLES];
    int ul_count = 0;
    for (int i = 0; i < r->throughput_count; i++) {
        if (strcmp(r->throughput_samples[i].direction, "upload") == 0) {
            if (r->throughput_samples[i].duration_ms >= 10.0) {
                ul_speeds[ul_count++] = r->throughput_samples[i].mbps;
            }
        }
    }

    if (ul_count > 0) {
        stats_sort(ul_speeds, ul_count);
        s->upload_mbps = stats_percentile(ul_speeds, ul_count, 90.0);
    }

    /* Collect latency samples by phase */
    double unloaded[MAX_SAMPLES];
    double download_lat[MAX_SAMPLES];
    double upload_lat[MAX_SAMPLES];
    int unloaded_count = 0, dl_lat_count = 0, ul_lat_count = 0;

    for (int i = 0; i < r->latency_count; i++) {
        double rtt = r->latency_samples[i].rtt_ms;
        const char *phase = r->latency_samples[i].phase;

        if (strcmp(phase, "unloaded") == 0) {
            unloaded[unloaded_count++] = rtt;
        } else if (strcmp(phase, "download") == 0) {
            download_lat[dl_lat_count++] = rtt;
        } else if (strcmp(phase, "upload") == 0) {
            upload_lat[ul_lat_count++] = rtt;
        }
    }

    /* Skip first 2 unloaded probes (cold start) */
    int skip = 2;
    double *unloaded_filtered = unloaded_count > skip ? unloaded + skip : unloaded;
    int unloaded_filtered_count = unloaded_count > skip ? unloaded_count - skip : unloaded_count;

    if (unloaded_filtered_count > 0) {
        stats_sort(unloaded_filtered, unloaded_filtered_count);
        s->latency_unloaded_ms = stats_percentile(unloaded_filtered, unloaded_filtered_count, 50.0);
        s->jitter_ms = stats_jitter(unloaded_filtered, unloaded_filtered_count);
    }

    if (dl_lat_count > 0) {
        stats_sort(download_lat, dl_lat_count);
        s->latency_download_ms = stats_percentile(download_lat, dl_lat_count, 90.0);
    }

    if (ul_lat_count > 0) {
        stats_sort(upload_lat, ul_lat_count);
        s->latency_upload_ms = stats_percentile(upload_lat, ul_lat_count, 90.0);
    }

    /* Packet loss */
    if (!r->packet_loss.unavailable) {
        s->packet_loss_percent = r->packet_loss.loss_percent;
    }
}

const char *grade_streaming(const summary_t *s)
{
    double dl = s->download_mbps;
    double lat = s->latency_unloaded_ms > 0 ? s->latency_unloaded_ms : 999;
    double jit = s->jitter_ms;
    double loss = s->packet_loss_percent;

    if (dl >= 50 && lat <= 25 && jit <= 5 && loss <= 0.5) return "Great";
    if (dl >= 20 && lat <= 50 && jit <= 15 && loss <= 1.5) return "Good";
    if (dl >= 10 && lat <= 80 && jit <= 30 && loss <= 3) return "Okay";
    return "Poor";
}

const char *grade_gaming(const summary_t *s)
{
    double dl = s->download_mbps;
    double lat = s->latency_unloaded_ms > 0 ? s->latency_unloaded_ms : 999;
    double jit = s->jitter_ms;
    double loss = s->packet_loss_percent;

    if (dl >= 25 && lat <= 20 && jit <= 3 && loss <= 0.1) return "Great";
    if (dl >= 15 && lat <= 40 && jit <= 10 && loss <= 0.5) return "Good";
    if (dl >= 5 && lat <= 80 && jit <= 20 && loss <= 2) return "Okay";
    return "Poor";
}

const char *grade_video_chat(const summary_t *s)
{
    double dl = s->download_mbps;
    double ul = s->upload_mbps;
    double lat = s->latency_unloaded_ms > 0 ? s->latency_unloaded_ms : 999;
    double jit = s->jitter_ms;
    double loss = s->packet_loss_percent;

    if (dl >= 10 && ul >= 5 && lat <= 50 && jit <= 10 && loss <= 1) return "Great";
    if (dl >= 5 && ul >= 2 && lat <= 100 && jit <= 20 && loss <= 2) return "Good";
    if (dl >= 2 && ul >= 1 && lat <= 150 && jit <= 40 && loss <= 5) return "Okay";
    return "Poor";
}

void calculate_quality(results_t *r)
{
    strncpy(r->quality.video_streaming, grade_streaming(&r->summary), 15);
    strncpy(r->quality.gaming, grade_gaming(&r->summary), 15);
    strncpy(r->quality.video_chatting, grade_video_chat(&r->summary), 15);
}
