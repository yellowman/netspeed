#include "output.h"

#include "json.h"
#include "stats.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define RESET "\033[0m"
#define RED "\033[31m"
#define GREEN "\033[32m"
#define YELLOW "\033[33m"
#define BLUE "\033[34m"
#define CYAN "\033[36m"
#define BOLD "\033[1m"

bool output_is_tty(void)
{
    const char *term = getenv("TERM");
    return isatty(STDOUT_FILENO) && !getenv("CI") && (!term || strcmp(term, "dumb") != 0);
}

void output_init(output_t *output, const config_t *config)
{
    memset(output, 0, sizeof(*output));
    output->json_mode = config->json_output;
    output->csv_mode = config->csv_output;
    output->quiet = config->quiet;
    output->verbose = config->verbose;
    output->interactive = output_is_tty() && !output->json_mode && !output->csv_mode && !output->quiet;
    output->use_color = output->interactive && !config->no_color;
}

static const char *color(const output_t *output, const char *code)
{
    return output->use_color ? code : "";
}

static const char *reset(const output_t *output)
{
    return output->use_color ? RESET : "";
}

void output_header(const output_t *output, const char *server_url)
{
    if (output->json_mode || output->csv_mode || output->quiet) return;
    printf("%snetspeed%s %s\n", color(output, BOLD), reset(output), NETSPEED_VERSION);
    printf("Server: %s%s%s\n", color(output, CYAN), server_url, reset(output));
    printf("────────────────────────────────────────────────\n");
}

void output_progress(const output_t *output, const char *stage,
                     int current, int total, double value)
{
    if (output->json_mode || output->csv_mode || output->quiet || total <= 0) return;
    if (!output->interactive) {
        if (output->verbose) printf("%s: %d/%d (%.1f)\n", stage, current, total, value);
        return;
    }
    double fraction = (double)current / (double)total;
    if (fraction < 0) fraction = 0;
    if (fraction > 1) fraction = 1;
    int filled = (int)(fraction * 20);
    char bar[24];
    int position = 0;
    for (int index = 0; index < filled && position < 20; index++) bar[position++] = '=';
    if (position < 20) bar[position++] = '>';
    while (position < 20) bar[position++] = ' ';
    bar[position] = '\0';
    const char *unit = strstr(stage, "latency") ? " ms" :
                       (!strcmp(stage, "download") || !strcmp(stage, "upload")) ? " Mbps" : "";
    printf("\r%-15s [%s] %3.0f%% %.1f%s\033[K", stage, bar, fraction * 100, value, unit);
    fflush(stdout);
}

void output_clear_progress(const output_t *output)
{
    if (output->interactive) {
        printf("\r%70s\r", "");
        fflush(stdout);
    }
}

static const char *grade_color(const output_t *output, const char *grade)
{
    if (!output->use_color) return "";
    if (!strcmp(grade, "Great") || !strcmp(grade, "Good")) return GREEN;
    if (!strcmp(grade, "Okay") || !strcmp(grade, "Incomplete")) return YELLOW;
    return RED;
}

static void print_grade(const output_t *output, const char *label, const char *grade)
{
    printf("    %-18s %s%s%s\n", label, grade_color(output, grade), grade, reset(output));
}

static void print_directional(const char *label, bool available, double loss,
                              int received, int sent)
{
    if (!available || sent <= 0) {
        printf("    %-12s N/A\n", label);
    } else {
        printf("    %-12s %.2f%% (%d/%d)\n", label, loss, received, sent);
    }
}

void output_results(const output_t *output, const results_t *results)
{
    output_clear_progress(output);
    printf("\n────────────────────────────────────────────────\n");
    printf("%s                    RESULTS%s\n", color(output, BOLD), reset(output));
    printf("────────────────────────────────────────────────\n");
    printf("  Download:     %s%.1f Mbps%s\n", color(output, CYAN),
           results->summary.download_mbps, reset(output));
    printf("  Upload:       %s%.1f Mbps%s\n", color(output, CYAN),
           results->summary.upload_mbps, reset(output));
    printf("  Latency:      %s%.1f ms (jitter: %.1f ms)%s\n", color(output, BLUE),
           results->summary.latency_unloaded_ms, results->summary.jitter_ms, reset(output));
    if (!results->packet_loss_present) {
        printf("  Packet Loss:  %sN/A (skipped)%s\n", color(output, YELLOW), reset(output));
    } else if (results->packet_loss.unavailable) {
        printf("  Packet Loss:  %sN/A (%s)%s\n", color(output, YELLOW),
               results->packet_loss.reason, reset(output));
    } else {
        printf("  Packet Loss:  %.2f%% transaction (%d/%d)\n",
               results->packet_loss.transaction_loss_percent,
               results->packet_loss.received, results->packet_loss.sent);
        print_directional("Forward:", results->packet_loss.forward_loss_available,
                          results->packet_loss.forward_loss_percent,
                          results->packet_loss.forward_received,
                          results->packet_loss.forward_sent);
        print_directional("Reverse ACK:", results->packet_loss.reverse_loss_available,
                          results->packet_loss.reverse_acknowledgement_loss_percent,
                          results->packet_loss.acknowledgements_received,
                          results->packet_loss.acknowledgements_sent);
    }
    printf("  Confidence:   %s (%d/100)\n",
           results->test_confidence.overall, results->test_confidence.overall_score);
    printf("────────────────────────────────────────────────\n");
    printf("  %sNetwork Quality:%s\n", color(output, BOLD), reset(output));
    print_grade(output, "Video Streaming:", results->quality.video_streaming);
    print_grade(output, "Online Gaming:", results->quality.gaming);
    print_grade(output, "Video Chatting:", results->quality.video_chatting);
    printf("────────────────────────────────────────────────\n");
}

void output_json(const results_t *results)
{
    char *encoded = results_to_json(results);
    if (!encoded) {
        fprintf(stderr, "{\"error\":\"failed to encode results\"}\n");
        return;
    }
    printf("%s\n", encoded);
    free(encoded);
}

void output_csv(const results_t *results)
{
    printf("timestamp,server,download_mbps,upload_mbps,latency_ms,jitter_ms,packet_loss_pct\n");
    printf("%s,%s,%.1f,%.1f,%.1f,%.1f,",
           results->start_time_rfc3339, results->meta.hostname,
           results->summary.download_mbps, results->summary.upload_mbps,
           results->summary.latency_unloaded_ms, results->summary.jitter_ms);
    if (results->summary.packet_loss_available) printf("%.2f", results->summary.packet_loss_percent);
    printf("\n");
}

void output_quiet(const results_t *results)
{
    printf("%.1f  %.1f  %.1f  ", results->summary.download_mbps,
           results->summary.upload_mbps, results->summary.latency_unloaded_ms);
    if (results->summary.packet_loss_available) printf("%.2f", results->summary.packet_loss_percent);
    else printf("N/A");
    printf("\n");
}

void output_error(const output_t *output, const char *message)
{
    if (output->json_mode) {
        json_writer_t writer;
        json_writer_init(&writer);
        json_start_object(&writer);
        json_kv_string(&writer, "error", message ? message : "unknown error");
        json_end_object(&writer);
        fprintf(stderr, "%s\n", json_writer_string(&writer));
        json_writer_free(&writer);
        return;
    }
    fprintf(stderr, "%sError:%s %s\n", color(output, RED), reset(output),
            message ? message : "unknown error");
}

static void print_samples(const results_t *results, const char *direction)
{
    for (int index = 0; index < results->throughput_count; index++) {
        const throughput_sample_t *sample = &results->throughput_samples[index];
        if (strcmp(sample->direction, direction) != 0) continue;
        if (!strcmp(sample->sample_kind, "window")) {
            printf("  window %d: %.1f Mbps; %d requests × %lld bytes; concurrency %d\n",
                   sample->window_index + 1, sample->mbps, sample->request_count,
                   (long long)sample->chunk_bytes, sample->concurrency);
        } else {
            printf("  %-8s run %d: %.1f Mbps (%lld bytes in %.1f ms)\n",
                   sample->profile, sample->run_index, sample->mbps,
                   (long long)sample->size_bytes, sample->duration_ms);
        }
    }
}

void output_verbose(const output_t *output, const results_t *results)
{
    if (!output->verbose) return;
    printf("\n%sLATENCY BREAKDOWN%s\n", color(output, BOLD), reset(output));
    printf("────────────────────────────────────────────────\n");
    printf("  unloaded median %.1f ms; loaded p90 download %.1f ms; upload %.1f ms\n",
           results->summary.latency_unloaded_ms, results->summary.latency_download_ms,
           results->summary.latency_upload_ms);
    printf("\n%sDOWNLOAD TESTS%s\n", color(output, BOLD), reset(output));
    printf("────────────────────────────────────────────────\n");
    print_samples(results, "download");
    printf("\n%sUPLOAD TESTS%s\n", color(output, BOLD), reset(output));
    printf("────────────────────────────────────────────────\n");
    print_samples(results, "upload");
    if (results->packet_loss_present && !results->packet_loss.unavailable) {
        printf("\n%sPACKET LOSS TEST%s\n", color(output, BOLD), reset(output));
        printf("────────────────────────────────────────────────\n");
        printf("  Frame:       %d bytes, exact binary v1\n", results->packet_loss.frame_size_bytes);
        printf("  Server:      duplicates %d, invalid %d, ACK send failures %d\n",
               results->packet_loss.duplicate_frames, results->packet_loss.invalid_frames,
               results->packet_loss.ack_send_failures);
        printf("  RTT:         min %.1f, median %.1f, p90 %.1f ms; jitter %.1f ms\n",
               results->packet_loss.rtt_stats_ms.min, results->packet_loss.rtt_stats_ms.median,
               results->packet_loss.rtt_stats_ms.p90, results->packet_loss.jitter_ms);
    }
    printf("\n%sMEASUREMENT CONFIDENCE%s\n", color(output, BOLD), reset(output));
    printf("────────────────────────────────────────────────\n");
    printf("  Overall:     %s (%d/100)\n", results->test_confidence.overall,
           results->test_confidence.overall_score);
    printf("  Samples:     windows d=%d u=%d; latency unloaded=%d loaded d=%d u=%d\n",
           results->test_confidence.sample_count.download_windows,
           results->test_confidence.sample_count.upload_windows,
           results->test_confidence.sample_count.unloaded_latency,
           results->test_confidence.sample_count.download_loaded_latency,
           results->test_confidence.sample_count.upload_loaded_latency);
    printf("  Variability: download %.1f%%, upload %.1f%%, latency %.1f%%\n",
           results->test_confidence.variability.download,
           results->test_confidence.variability.upload,
           results->test_confidence.variability.latency);
    for (int index = 0; index < results->test_confidence.warning_count; index++) {
        printf("  Warning:     %s\n", results->test_confidence.warnings[index]);
    }
}
