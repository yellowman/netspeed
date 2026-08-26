#include "stats.h"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int compare_double(const void *left, const void *right)
{
    double a = *(const double *)left;
    double b = *(const double *)right;
    return (a > b) - (a < b);
}

void stats_sort(double *values, size_t count)
{
    if (count > 1) {
        qsort(values, count, sizeof(*values), compare_double);
    }
}

size_t stats_clean(const double *values, size_t count, double *out, size_t out_capacity)
{
    size_t written = 0;
    for (size_t index = 0; index < count && written < out_capacity; index++) {
        double value = values[index];
        if (value > 0 && isfinite(value)) {
            out[written++] = value;
        }
    }
    return written;
}

static double percentile_sorted(const double *sorted, size_t count, double percentile)
{
    if (count == 0) {
        return 0;
    }
    if (percentile <= 0) {
        return sorted[0];
    }
    if (percentile >= 100) {
        return sorted[count - 1];
    }
    double rank = percentile / 100.0 * (double)(count - 1);
    size_t lower = (size_t)floor(rank);
    size_t upper = (size_t)ceil(rank);
    if (lower == upper) {
        return sorted[lower];
    }
    double weight = rank - (double)lower;
    return sorted[lower] + (sorted[upper] - sorted[lower]) * weight;
}

double stats_percentile(const double *values, size_t count, double percentile)
{
    double *clean = calloc(count ? count : 1, sizeof(*clean));
    if (!clean) {
        return 0;
    }
    size_t clean_count = stats_clean(values, count, clean, count);
    stats_sort(clean, clean_count);
    double result = percentile_sorted(clean, clean_count, percentile);
    free(clean);
    return result;
}

size_t stats_filter_iqr(const double *values, size_t count, double *out, size_t out_capacity)
{
    if (out_capacity == 0) {
        return 0;
    }
    double *clean = calloc(count ? count : 1, sizeof(*clean));
    double *sorted = calloc(count ? count : 1, sizeof(*sorted));
    if (!clean || !sorted) {
        free(clean);
        free(sorted);
        return 0;
    }
    size_t clean_count = stats_clean(values, count, clean, count);
    if (clean_count < 4) {
        size_t copied = clean_count < out_capacity ? clean_count : out_capacity;
        memcpy(out, clean, copied * sizeof(*out));
        free(clean);
        free(sorted);
        return copied;
    }
    memcpy(sorted, clean, clean_count * sizeof(*sorted));
    stats_sort(sorted, clean_count);
    double q1 = percentile_sorted(sorted, clean_count, 25);
    double q3 = percentile_sorted(sorted, clean_count, 75);
    double iqr = q3 - q1;
    double lower = q1 - 1.5 * iqr;
    double upper = q3 + 1.5 * iqr;
    size_t filtered_count = 0;
    for (size_t index = 0; index < clean_count && filtered_count < out_capacity; index++) {
        if (clean[index] >= lower && clean[index] <= upper) {
            out[filtered_count++] = clean[index];
        }
    }
    if (filtered_count * 2 < clean_count) {
        filtered_count = clean_count < out_capacity ? clean_count : out_capacity;
        memcpy(out, clean, filtered_count * sizeof(*out));
    }
    free(clean);
    free(sorted);
    return filtered_count;
}

size_t stats_prepare_latency(const double *values, size_t count, size_t warmup,
                             double *out, size_t out_capacity)
{
    double *clean = calloc(count ? count : 1, sizeof(*clean));
    if (!clean) {
        return 0;
    }
    size_t clean_count = stats_clean(values, count, clean, count);
    if (warmup >= clean_count) {
        free(clean);
        return 0;
    }
    size_t result = stats_filter_iqr(clean + warmup, clean_count - warmup, out, out_capacity);
    free(clean);
    return result;
}

double stats_jitter(const double *values, size_t count)
{
    if (count == 0) {
        return 0;
    }
    return stats_percentile(values, count, 90) - stats_percentile(values, count, 50);
}

double stats_coefficient_of_variation(const double *values, size_t count)
{
    double *clean = calloc(count ? count : 1, sizeof(*clean));
    if (!clean) {
        return 0;
    }
    size_t clean_count = stats_clean(values, count, clean, count);
    if (clean_count < 2) {
        free(clean);
        return 0;
    }
    double sum = 0;
    for (size_t index = 0; index < clean_count; index++) {
        sum += clean[index];
    }
    double mean = sum / (double)clean_count;
    if (mean <= 0) {
        free(clean);
        return 0;
    }
    double squared = 0;
    for (size_t index = 0; index < clean_count; index++) {
        double delta = clean[index] - mean;
        squared += delta * delta;
    }
    double result = sqrt(squared / (double)clean_count) / mean * 100.0;
    free(clean);
    return result;
}

double bytes_to_mbps(int64_t bytes, double duration_ms)
{
    if (bytes <= 0 || duration_ms <= 0 || !isfinite(duration_ms)) {
        return 0;
    }
    return (double)bytes * 8.0 / duration_ms / 1000.0;
}

static size_t collect_throughput(const results_t *results, const char *direction,
                                 double *out, size_t capacity)
{
    double fallback[MAX_SAMPLES];
    size_t window_count = 0;
    size_t fallback_count = 0;
    for (int index = 0; index < results->throughput_count; index++) {
        const throughput_sample_t *sample = &results->throughput_samples[index];
        if (strcmp(sample->direction, direction) != 0 || sample->duration_ms < 10 ||
            sample->mbps <= 0 || !isfinite(sample->mbps)) {
            continue;
        }
        fallback[fallback_count++] = sample->mbps;
        if (strcmp(sample->sample_kind, "window") == 0 ||
            strcmp(sample->profile, "window") == 0) {
            out[window_count++] = sample->mbps;
        }
    }
    if (window_count > 0) {
        double filtered[MAX_SAMPLES];
        size_t count = stats_filter_iqr(out, window_count, filtered, capacity);
        memcpy(out, filtered, count * sizeof(*out));
        return count;
    }
    return stats_filter_iqr(fallback, fallback_count, out, capacity);
}

static size_t collect_latency(const results_t *results, const char *condition,
                              bool require_overlap, double *out, size_t capacity)
{
    size_t count = 0;
    for (int index = 0; index < results->latency_count && count < capacity; index++) {
        const latency_sample_t *sample = &results->latency_samples[index];
        if (strcmp(sample->condition, condition) != 0 ||
            (require_overlap && !sample->load_overlapped)) {
            continue;
        }
        if (sample->rtt_ms > 0 && isfinite(sample->rtt_ms)) {
            out[count++] = sample->rtt_ms;
        }
    }
    return count;
}

void calculate_summary(results_t *results)
{
    memset(&results->summary, 0, sizeof(results->summary));
    double download[MAX_SAMPLES], upload[MAX_SAMPLES];
    double unloaded_raw[MAX_SAMPLES], unloaded[MAX_SAMPLES];
    double download_loaded_raw[MAX_SAMPLES], download_loaded[MAX_SAMPLES];
    double upload_loaded_raw[MAX_SAMPLES], upload_loaded[MAX_SAMPLES];

    size_t download_count = collect_throughput(results, "download", download, MAX_SAMPLES);
    size_t upload_count = collect_throughput(results, "upload", upload, MAX_SAMPLES);
    size_t unloaded_raw_count = collect_latency(results, "unloaded", false, unloaded_raw, MAX_SAMPLES);
    size_t download_loaded_raw_count = collect_latency(results, "download", true, download_loaded_raw, MAX_SAMPLES);
    size_t upload_loaded_raw_count = collect_latency(results, "upload", true, upload_loaded_raw, MAX_SAMPLES);
    size_t unloaded_count = stats_prepare_latency(unloaded_raw, unloaded_raw_count, 2, unloaded, MAX_SAMPLES);
    size_t download_loaded_count = stats_filter_iqr(download_loaded_raw, download_loaded_raw_count,
                                                    download_loaded, MAX_SAMPLES);
    size_t upload_loaded_count = stats_filter_iqr(upload_loaded_raw, upload_loaded_raw_count,
                                                  upload_loaded, MAX_SAMPLES);

    results->summary.download_mbps = stats_percentile(download, download_count, 90);
    results->summary.upload_mbps = stats_percentile(upload, upload_count, 90);
    results->summary.latency_unloaded_ms = stats_percentile(unloaded, unloaded_count, 50);
    results->summary.latency_download_ms = stats_percentile(download_loaded, download_loaded_count, 90);
    results->summary.latency_upload_ms = stats_percentile(upload_loaded, upload_loaded_count, 90);
    results->summary.jitter_ms = stats_jitter(unloaded, unloaded_count);
    if (results->packet_loss_present && !results->packet_loss.unavailable) {
        results->summary.packet_loss_available = true;
        results->summary.packet_loss_percent = results->packet_loss.transaction_loss_percent;
    }
}

static void set_grade(char *destination, size_t capacity, const char *grade)
{
    snprintf(destination, capacity, "%s", grade);
}

void calculate_quality(results_t *results)
{
    const summary_t *summary = &results->summary;
    if (!summary->packet_loss_available) {
        set_grade(results->quality.video_streaming, sizeof(results->quality.video_streaming), "Incomplete");
        set_grade(results->quality.gaming, sizeof(results->quality.gaming), "Incomplete");
        set_grade(results->quality.video_chatting, sizeof(results->quality.video_chatting), "Incomplete");
        return;
    }
    double loss = summary->packet_loss_percent;
    const char *streaming = "Poor";
    if (summary->download_mbps >= 50 && summary->latency_unloaded_ms <= 25 &&
        summary->jitter_ms <= 5 && loss <= 0.5) {
        streaming = "Great";
    } else if (summary->download_mbps >= 20 && summary->latency_unloaded_ms <= 50 &&
               summary->jitter_ms <= 15 && loss <= 1.5) {
        streaming = "Good";
    } else if (summary->download_mbps >= 10 && summary->latency_unloaded_ms <= 80 &&
               summary->jitter_ms <= 30 && loss <= 3) {
        streaming = "Okay";
    }
    const char *gaming = "Poor";
    if (summary->download_mbps >= 25 && summary->latency_unloaded_ms <= 20 &&
        summary->jitter_ms <= 5 && loss <= 0.1) {
        gaming = "Great";
    } else if (summary->download_mbps >= 15 && summary->latency_unloaded_ms <= 40 &&
               summary->jitter_ms <= 10 && loss <= 0.5) {
        gaming = "Good";
    } else if (summary->download_mbps >= 5 && summary->latency_unloaded_ms <= 80 &&
               summary->jitter_ms <= 20 && loss <= 1) {
        gaming = "Okay";
    }
    const char *video_chat = "Poor";
    if (summary->download_mbps >= 10 && summary->upload_mbps >= 5 &&
        summary->latency_unloaded_ms <= 50 && summary->jitter_ms <= 10 && loss <= 1) {
        video_chat = "Great";
    } else if (summary->download_mbps >= 5 && summary->upload_mbps >= 2 &&
               summary->latency_unloaded_ms <= 100 && summary->jitter_ms <= 20 && loss <= 2) {
        video_chat = "Good";
    } else if (summary->download_mbps >= 2 && summary->upload_mbps >= 1 &&
               summary->latency_unloaded_ms <= 150 && summary->jitter_ms <= 40 && loss <= 5) {
        video_chat = "Okay";
    }
    set_grade(results->quality.video_streaming, sizeof(results->quality.video_streaming), streaming);
    set_grade(results->quality.gaming, sizeof(results->quality.gaming), gaming);
    set_grade(results->quality.video_chatting, sizeof(results->quality.video_chatting), video_chat);
}

static int count_windows(const results_t *results, const char *direction)
{
    int count = 0;
    for (int index = 0; index < results->throughput_count; index++) {
        const throughput_sample_t *sample = &results->throughput_samples[index];
        if (strcmp(sample->direction, direction) == 0 &&
            (strcmp(sample->sample_kind, "window") == 0 || strcmp(sample->profile, "window") == 0)) {
            count++;
        }
    }
    return count;
}

static int count_latency(const results_t *results, const char *condition, bool overlap_only)
{
    int count = 0;
    for (int index = 0; index < results->latency_count; index++) {
        const latency_sample_t *sample = &results->latency_samples[index];
        if (strcmp(sample->condition, condition) == 0 && (!overlap_only || sample->load_overlapped)) {
            count++;
        }
    }
    return count;
}

static void confidence_warning(test_confidence_t *confidence, const char *warning)
{
    if (confidence->warning_count >= MAX_WARNINGS) {
        return;
    }
    snprintf(confidence->warnings[confidence->warning_count], MAX_WARNING_LEN, "%s", warning);
    confidence->warning_count++;
}

void assess_test_confidence(results_t *results, const config_t *config)
{
    test_confidence_t *confidence = &results->test_confidence;
    memset(confidence, 0, sizeof(*confidence));
    bool download_expected = !config->upload_only;
    bool upload_expected = !config->download_only;

    double download[MAX_SAMPLES], upload[MAX_SAMPLES];
    double unloaded_raw[MAX_SAMPLES], unloaded[MAX_SAMPLES];
    size_t download_count = collect_throughput(results, "download", download, MAX_SAMPLES);
    size_t upload_count = collect_throughput(results, "upload", upload, MAX_SAMPLES);
    size_t unloaded_raw_count = collect_latency(results, "unloaded", false, unloaded_raw, MAX_SAMPLES);
    size_t unloaded_count = stats_prepare_latency(unloaded_raw, unloaded_raw_count, 2, unloaded, MAX_SAMPLES);

    confidence->sample_count.download_windows = count_windows(results, "download");
    confidence->sample_count.upload_windows = count_windows(results, "upload");
    confidence->sample_count.unloaded_latency = count_latency(results, "unloaded", false);
    confidence->sample_count.download_loaded_latency = count_latency(results, "download", true);
    confidence->sample_count.upload_loaded_latency = count_latency(results, "upload", true);
    bool adequate = confidence->sample_count.unloaded_latency >= 10;
    if (download_expected) {
        adequate = adequate && confidence->sample_count.download_windows >= 3 &&
                   confidence->sample_count.download_loaded_latency >= 3;
    }
    if (upload_expected) {
        adequate = adequate && confidence->sample_count.upload_windows >= 3 &&
                   confidence->sample_count.upload_loaded_latency >= 3;
    }
    confidence->sample_count.adequate = adequate;

    confidence->variability.download = stats_coefficient_of_variation(download, download_count);
    confidence->variability.upload = stats_coefficient_of_variation(upload, upload_count);
    confidence->variability.latency = stats_coefficient_of_variation(unloaded, unloaded_count);
    bool variability = confidence->variability.latency < 50;
    if (download_expected) {
        variability = variability && confidence->variability.download < 30;
    }
    if (upload_expected) {
        variability = variability && confidence->variability.upload < 30;
    }
    confidence->variability.acceptable = variability;

    confidence->loaded_overlap.download_accepted = confidence->sample_count.download_loaded_latency;
    confidence->loaded_overlap.upload_accepted = confidence->sample_count.upload_loaded_latency;
    bool overlap = true;
    if (download_expected) {
        overlap = overlap && confidence->loaded_overlap.download_accepted >= 3;
    }
    if (upload_expected) {
        overlap = overlap && confidence->loaded_overlap.upload_accepted >= 3;
    }
    confidence->loaded_overlap.complete = overlap;

    confidence->packet_test_completed = results->packet_loss_present && !results->packet_loss.unavailable &&
                                        results->packet_loss.forward_loss_available &&
                                        results->packet_loss.acknowledgements_sent > 0 &&
                                        results->packet_loss.reverse_loss_available;
    confidence->timing_accurate = true;
    for (int index = 0; index < results->throughput_count; index++) {
        const throughput_sample_t *sample = &results->throughput_samples[index];
        if (strcmp(sample->sample_kind, "window") == 0 &&
            strcmp(sample->timing_source, "aggregate-wall-clock") != 0) {
            confidence->timing_accurate = false;
        }
    }
    for (int index = 0; index < results->latency_count; index++) {
        const latency_sample_t *sample = &results->latency_samples[index];
        if (sample->load_overlapped && !sample->load_tracking_accurate) {
            confidence->timing_accurate = false;
        }
    }

    int score = 100;
    if (!adequate) {
        score -= 20;
        confidence_warning(confidence, "Insufficient fixed-window or latency samples for high confidence");
    }
    if (!variability) {
        score -= 25;
        confidence_warning(confidence, "High variability in measurements");
    }
    if (!overlap) {
        score -= 25;
        confidence_warning(confidence, "Loaded-latency overlap was incomplete");
    }
    if (!confidence->packet_test_completed) {
        score -= 20;
        confidence_warning(confidence, "Directional packet-loss test incomplete");
    }
    if (!confidence->timing_accurate) {
        score -= 10;
        confidence_warning(confidence, "Some measurements used fallback timing");
    }
    if (score < 0) {
        score = 0;
    }
    confidence->overall_score = score;
    snprintf(confidence->overall, sizeof(confidence->overall), "%s",
             score >= 80 ? "high" : score >= 50 ? "medium" : "low");
}
