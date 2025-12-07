/*
 * output.c - Terminal output, spinners, and colors
 */

#include "output.h"
#include "json.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <time.h>

/* Spinner frames */
static const char *spinner_frames[] = {"|", "/", "-", "\\"};
#define SPINNER_FRAME_COUNT 4

/* Color buffer for wrapping */
static char color_buf[256];

bool output_is_tty(void)
{
    /* Check for CI environment */
    if (getenv("CI") != NULL) {
        return false;
    }

    /* Check for dumb terminal */
    const char *term = getenv("TERM");
    if (term && strcmp(term, "dumb") == 0) {
        return false;
    }

    return isatty(STDOUT_FILENO);
}

void output_init(output_t *out, const config_t *cfg)
{
    out->use_color = !cfg->no_color && output_is_tty();
    out->interactive = output_is_tty();
    out->quiet = cfg->quiet;
    out->verbose = cfg->verbose;
    out->json_mode = cfg->json_output;
    out->csv_mode = cfg->csv_output;
}

const char *color_wrap(const output_t *out, const char *color, const char *text)
{
    if (!out->use_color) {
        return text;
    }
    snprintf(color_buf, sizeof(color_buf), "%s%s%s", color, text, COLOR_RESET);
    return color_buf;
}

const char *grade_color(const output_t *out, const char *grade)
{
    if (!out->use_color) {
        return grade;
    }

    const char *color;
    if (strcmp(grade, "Great") == 0 || strcmp(grade, "Good") == 0) {
        color = COLOR_GREEN;
    } else if (strcmp(grade, "Okay") == 0) {
        color = COLOR_YELLOW;
    } else {
        color = COLOR_RED;
    }

    snprintf(color_buf, sizeof(color_buf), "%s%s%s", color, grade, COLOR_RESET);
    return color_buf;
}

void output_header(const output_t *out, const char *server_url)
{
    if (out->json_mode || out->csv_mode || out->quiet) {
        return;
    }

    printf("%snetspeed%s v%s\n",
           out->use_color ? COLOR_BOLD : "",
           out->use_color ? COLOR_RESET : "",
           NETSPEED_VERSION);

    printf("Server: %s%s%s\n",
           out->use_color ? COLOR_CYAN : "",
           server_url,
           out->use_color ? COLOR_RESET : "");

    printf("────────────────────────────────────────────────\n");
}

void progress_bar(double percent, int width, char *buf, size_t buf_len)
{
    if (percent < 0) percent = 0;
    if (percent > 1) percent = 1;

    int filled = (int)(percent * width);
    int empty = width - filled;

    int pos = 0;
    for (int i = 0; i < filled && pos < (int)buf_len - 2; i++) {
        buf[pos++] = '=';
    }
    if (filled < width && pos < (int)buf_len - 2) {
        buf[pos++] = '>';
        empty--;
    }
    for (int i = 0; i < empty && pos < (int)buf_len - 2; i++) {
        buf[pos++] = ' ';
    }
    buf[pos] = '\0';
}

void output_progress(const output_t *out, const char *phase,
                     int current, int total, double value)
{
    if (out->json_mode || out->csv_mode || out->quiet) {
        return;
    }

    if (!out->interactive) {
        if (out->verbose) {
            printf("%s: %d/%d (%.1f)\n", phase, current, total, value);
        }
        return;
    }

    double percent = (total > 0) ? (double)current / (double)total : 0.0;
    char bar[32];
    progress_bar(percent, 20, bar, sizeof(bar));

    const char *unit = "";
    if (strcmp(phase, "download") == 0 || strcmp(phase, "upload") == 0) {
        unit = " Mbps";
    } else if (strcmp(phase, "latency") == 0) {
        unit = " ms";
    }

    /* Capitalize first letter */
    char label[32];
    strncpy(label, phase, sizeof(label) - 1);
    label[sizeof(label) - 1] = '\0';
    if (label[0] >= 'a' && label[0] <= 'z') {
        label[0] -= 32;
    }

    printf("\r%-12s [%s] %3.0f%% %.1f%s", label, bar, percent * 100, value, unit);
    fflush(stdout);
}

void output_clear_progress(const output_t *out)
{
    if (out->interactive) {
        printf("\r%60s\r", "");
        fflush(stdout);
    }
}

void output_error(const output_t *out, const char *msg)
{
    if (out->json_mode) {
        fprintf(stderr, "{\"error\": \"%s\"}\n", msg);
        return;
    }

    fprintf(stderr, "%sError:%s %s\n",
            out->use_color ? COLOR_RED : "",
            out->use_color ? COLOR_RESET : "",
            msg);
}

void output_results(const output_t *out, const results_t *r)
{
    output_clear_progress(out);

    printf("\n");
    printf("────────────────────────────────────────────────\n");
    printf("%s                    RESULTS%s\n",
           out->use_color ? COLOR_BOLD : "",
           out->use_color ? COLOR_RESET : "");
    printf("────────────────────────────────────────────────\n");

    /* Download */
    printf("  Download:     %s%.1f Mbps%s\n",
           out->use_color ? COLOR_CYAN : "",
           r->summary.download_mbps,
           out->use_color ? COLOR_RESET : "");

    /* Upload */
    printf("  Upload:       %s%.1f Mbps%s\n",
           out->use_color ? COLOR_CYAN : "",
           r->summary.upload_mbps,
           out->use_color ? COLOR_RESET : "");

    /* Latency */
    printf("  Latency:      %s%.1f ms (jitter: %.1f ms)%s\n",
           out->use_color ? COLOR_BLUE : "",
           r->summary.latency_unloaded_ms,
           r->summary.jitter_ms,
           out->use_color ? COLOR_RESET : "");

    /* Packet Loss */
    if (r->packet_loss.unavailable) {
        printf("  Packet Loss:  %sN/A (%s)%s\n",
               out->use_color ? COLOR_YELLOW : "",
               r->packet_loss.reason,
               out->use_color ? COLOR_RESET : "");
    } else {
        printf("  Packet Loss:  %.2f%% (%d/%d)\n",
               r->packet_loss.loss_percent,
               r->packet_loss.received,
               r->packet_loss.sent);
    }

    printf("────────────────────────────────────────────────\n");

    /* Network Quality */
    printf("  %sNetwork Quality:%s\n",
           out->use_color ? COLOR_BOLD : "",
           out->use_color ? COLOR_RESET : "");
    printf("    Video Streaming:  %s\n", grade_color(out, r->quality.video_streaming));
    printf("    Online Gaming:    %s\n", grade_color(out, r->quality.gaming));
    printf("    Video Chatting:   %s\n", grade_color(out, r->quality.video_chatting));

    printf("────────────────────────────────────────────────\n");
}

void output_verbose(const output_t *out, const results_t *r)
{
    if (!out->verbose) {
        return;
    }

    printf("\n");
    printf("%sLATENCY BREAKDOWN%s\n",
           out->use_color ? COLOR_BOLD : "",
           out->use_color ? COLOR_RESET : "");
    printf("────────────────────────────────────────────────\n");

    /* Collect unloaded samples */
    double unloaded[MAX_SAMPLES];
    int unloaded_count = 0;

    for (int i = 0; i < r->latency_count; i++) {
        if (strcmp(r->latency_samples[i].phase, "unloaded") == 0) {
            unloaded[unloaded_count++] = r->latency_samples[i].rtt_ms;
        }
    }

    if (unloaded_count > 0) {
        double min = unloaded[0], max = unloaded[0], sum = 0;
        for (int i = 0; i < unloaded_count; i++) {
            if (unloaded[i] < min) min = unloaded[i];
            if (unloaded[i] > max) max = unloaded[i];
            sum += unloaded[i];
        }
        printf("  Unloaded:   %.1f ms (min: %.1f, max: %.1f, avg: %.1f)\n",
               r->summary.latency_unloaded_ms, min, max, sum / unloaded_count);
    }

    printf("\n");
    printf("%sDOWNLOAD TESTS%s\n",
           out->use_color ? COLOR_BOLD : "",
           out->use_color ? COLOR_RESET : "");
    printf("────────────────────────────────────────────────\n");

    /* Group download samples by profile */
    const char *profiles[] = {"100k", "1m", "10m", "25m", "100m"};
    int num_profiles = 5;

    for (int p = 0; p < num_profiles; p++) {
        double speeds[MAX_SAMPLES];
        int count = 0;

        for (int i = 0; i < r->throughput_count; i++) {
            const throughput_sample_t *s = &r->throughput_samples[i];
            if (strcmp(s->direction, "download") == 0 &&
                strcmp(s->profile, profiles[p]) == 0) {
                speeds[count++] = s->mbps;
            }
        }

        if (count > 0) {
            double min = speeds[0], max = speeds[0], sum = 0;
            for (int i = 0; i < count; i++) {
                if (speeds[i] < min) min = speeds[i];
                if (speeds[i] > max) max = speeds[i];
                sum += speeds[i];
            }
            printf("  %-8s x%d:  avg %.1f Mbps  (min: %.0f, max: %.0f)\n",
                   profiles[p], count, sum / count, min, max);
        }
    }

    printf("\n");
    printf("%sUPLOAD TESTS%s\n",
           out->use_color ? COLOR_BOLD : "",
           out->use_color ? COLOR_RESET : "");
    printf("────────────────────────────────────────────────\n");

    for (int p = 0; p < num_profiles; p++) {
        double speeds[MAX_SAMPLES];
        int count = 0;

        for (int i = 0; i < r->throughput_count; i++) {
            const throughput_sample_t *s = &r->throughput_samples[i];
            if (strcmp(s->direction, "upload") == 0 &&
                strcmp(s->profile, profiles[p]) == 0) {
                speeds[count++] = s->mbps;
            }
        }

        if (count > 0) {
            double min = speeds[0], max = speeds[0], sum = 0;
            for (int i = 0; i < count; i++) {
                if (speeds[i] < min) min = speeds[i];
                if (speeds[i] > max) max = speeds[i];
                sum += speeds[i];
            }
            printf("  %-8s x%d:  avg %.1f Mbps  (min: %.0f, max: %.0f)\n",
                   profiles[p], count, sum / count, min, max);
        }
    }

    if (!r->packet_loss.unavailable) {
        printf("\n");
        printf("%sPACKET LOSS TEST%s\n",
               out->use_color ? COLOR_BOLD : "",
               out->use_color ? COLOR_RESET : "");
        printf("────────────────────────────────────────────────\n");
        printf("  Sent:       %d packets\n", r->packet_loss.sent);
        printf("  Received:   %d packets\n", r->packet_loss.received);
        printf("  Loss:       %.2f%%\n", r->packet_loss.loss_percent);
        printf("  RTT:        min %.1f ms, median %.1f ms, p90 %.1f ms\n",
               r->packet_loss.rtt_stats_ms.min,
               r->packet_loss.rtt_stats_ms.median,
               r->packet_loss.rtt_stats_ms.p90);
        printf("  Jitter:     %.1f ms\n", r->packet_loss.jitter_ms);
    }
}

void output_json(const results_t *r)
{
    char *json = results_to_json(r);
    if (json) {
        printf("%s\n", json);
        free(json);
    }
}

void output_csv(const results_t *r)
{
    /* Header */
    printf("timestamp,server,download_mbps,upload_mbps,latency_ms,jitter_ms,packet_loss_pct\n");

    /* Format timestamp as ISO8601 */
    time_t ts = r->start_time / 1000;
    struct tm *tm = gmtime(&ts);
    char time_buf[32];
    strftime(time_buf, sizeof(time_buf), "%Y-%m-%dT%H:%M:%SZ", tm);

    /* Data row */
    printf("%s,%s,%.1f,%.1f,%.1f,%.1f,%.2f\n",
           time_buf,
           r->meta.hostname,
           r->summary.download_mbps,
           r->summary.upload_mbps,
           r->summary.latency_unloaded_ms,
           r->summary.jitter_ms,
           r->summary.packet_loss_percent);
}

void output_quiet(const results_t *r)
{
    /* Format: download_mbps upload_mbps latency_ms loss_percent */
    printf("%.1f  %.1f  %.1f  %.2f\n",
           r->summary.download_mbps,
           r->summary.upload_mbps,
           r->summary.latency_unloaded_ms,
           r->summary.packet_loss_percent);
}

/* ===================== Spinner ===================== */

void spinner_init(spinner_t *s)
{
    s->frames = spinner_frames;
    s->frame_count = SPINNER_FRAME_COUNT;
    s->current = 0;
    s->prefix[0] = '\0';
    s->running = false;
}

void spinner_start(spinner_t *s, const char *prefix)
{
    strncpy(s->prefix, prefix, sizeof(s->prefix) - 1);
    s->prefix[sizeof(s->prefix) - 1] = '\0';
    s->current = 0;
    s->running = true;
}

void spinner_update(spinner_t *s)
{
    if (!s->running) return;

    printf("\r%s %s", s->prefix, s->frames[s->current]);
    fflush(stdout);
    s->current = (s->current + 1) % s->frame_count;
}

void spinner_stop(spinner_t *s, const char *final_text)
{
    s->running = false;
    printf("\r%s\n", final_text);
}
