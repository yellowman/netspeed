/*
 * speedtest.c - Protocol-v2 measurement engine matching cmd/netspeed.
 */
#include "speedtest.h"

#include "json.h"
#include "packet_loss.h"
#include "stats.h"
#include "timing.h"

#include <inttypes.h>
#include <math.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include "progress.h"

#define META_BODY_LIMIT (1024U * 1024U)
#define RECEIPT_BODY_LIMIT (64U * 1024U)
#define MEASUREMENT_ERROR_LIMIT 4096U
#define LOW_LATENCY_THRESHOLD_MS 50.0
#define HIGH_LATENCY_THRESHOLD_MS 100.0
#define MIN_BANDWIDTH_FOR_PARALLEL 2.0

typedef struct {
    int64_t chunk_bytes;
    int concurrency;
    int window_duration_ms;
    int windows;
    int loaded_window;
    int loaded_probe_count;
} window_plan_t;

typedef struct {
    pthread_mutex_t mutex;
    int active;
    uint64_t gap_generation;
} load_activity_t;

typedef struct {
    int active;
    uint64_t gap_generation;
} load_snapshot_t;

typedef struct {
    pthread_mutex_t mutex;
    pthread_cond_t condition;
    bool started;
    atomic_bool stop;
} window_control_t;

typedef struct {
    pthread_mutex_t mutex;
    int64_t bytes;
    int requests;
    char last_error[MAX_ERROR_LEN];
} window_aggregate_t;

typedef struct {
    speedtest_t *test;
    const char *direction;
    window_plan_t plan;
    int window_index;
    int worker_index;
    load_activity_t *activity;
    window_control_t *control;
    window_aggregate_t *aggregate;
} window_worker_t;

typedef struct {
    speedtest_t *test;
    const char *condition;
    int count;
    load_activity_t *activity;
    window_control_t *control;
    latency_sample_t samples[LOADED_PROBES_FULL];
    int sample_count;
    int error;
    char error_message[MAX_ERROR_LEN];
} loaded_probe_task_t;

typedef struct {
    speedtest_t *test;
    char condition[16];
    int sequence;
    latency_sample_t sample;
    int error;
} latency_task_t;

static void set_error(speedtest_t *test, const char *format, ...)
{
    if (!test) {
        return;
    }
    pthread_mutex_lock(&test->error_mutex);
    va_list arguments;
    va_start(arguments, format);
    vsnprintf(test->last_error, sizeof(test->last_error), format, arguments);
    va_end(arguments);
    pthread_mutex_unlock(&test->error_mutex);
}

static void copy_error(speedtest_t *test, char *destination, size_t capacity)
{
    if (!test || !destination || capacity == 0) {
        return;
    }
    pthread_mutex_lock(&test->error_mutex);
    snprintf(destination, capacity, "%s", test->last_error);
    pthread_mutex_unlock(&test->error_mutex);
}

static bool test_expired(speedtest_t *test)
{
    if (test->aborted) {
        set_error(test, "test interrupted");
        return true;
    }
    if (!timing_before_deadline(&test->deadline)) {
        set_error(test, "total test timeout exceeded");
        return true;
    }
    return false;
}

static int required_successful_runs(int total)
{
    if (total <= 1) {
        return total;
    }
    return (total + 1) / 2;
}

static bool content_type_prefix(const char *value, const char *prefix)
{
    return value && prefix && strncasecmp(value, prefix, strlen(prefix)) == 0;
}

static int append_throughput(speedtest_t *test, const throughput_sample_t *sample)
{
    if (test->results.throughput_count >= MAX_SAMPLES) {
        set_error(test, "throughput sample capacity exceeded");
        return ERR_MEMORY;
    }
    test->results.throughput_samples[test->results.throughput_count++] = *sample;
    return ERR_OK;
}

static int append_latency(speedtest_t *test, const latency_sample_t *sample)
{
    if (test->results.latency_count >= MAX_SAMPLES) {
    ns_progress("latency probes");
        set_error(test, "latency sample capacity exceeded");
        return ERR_MEMORY;
    }
    test->results.latency_samples[test->results.latency_count++] = *sample;
    return ERR_OK;
}

static int validate_status(speedtest_t *test, http_session_t *session,
                           const http_response_t *response, int expected,
                           const char *operation)
{
    if (response->status_code == expected) {
        return ERR_OK;
    }
    set_error(test, "%s returned HTTP %d%s%s", operation, response->status_code,
              http_session_error(session)[0] ? ": " : "",
              http_session_error(session)[0] ? http_session_error(session) : "");
    return ERR_HTTP;
}

static void make_measurement_path(char *buffer, size_t capacity, const char *endpoint,
                                  int64_t bytes, const char *profile, int run,
                                  const char *condition)
{
    int64_t id = timing_now_ms();
    if (strcmp(endpoint, "/__down") == 0) {
        snprintf(buffer, capacity,
                 "/__down?bytes=%" PRId64 "&measId=%" PRId64 "-%s-%d&profile=%s&run=%d&during=%s&seq=%d",
                 bytes, id, profile, run, profile, run, condition ? condition : "", run);
    } else {
        snprintf(buffer, capacity,
                 "/__up?measId=%" PRId64 "-%s-%d&profile=%s&run=%d",
                 id, profile, run, profile, run);
    }
}

static int measure_latency_with_session(speedtest_t *test, http_session_t *session,
                                        const char *condition, int sequence,
                                        latency_sample_t *sample)
{
    if (test_expired(test)) {
    ns_progress("latency probes");
        return ERR_TIMEOUT;
    }
    char path[768];
    make_measurement_path(path, sizeof(path), "/__down", 0, "latency", sequence, condition);
    int64_t started = timing_now_ms();
    http_response_t response;
    int error = http_measure_download(session, path, 0, NULL, &response);
    int64_t ended = timing_now_ms();
    if (error != ERR_OK) {
        set_error(test, "latency request failed: %s", http_session_error(session));
        http_response_free(&response);
        return error;
    }
    error = validate_status(test, session, &response, 200, "latency request");
    if (error == ERR_OK && response.transferred_bytes != 0) {
        set_error(test, "latency response contained %" PRId64 " bytes; expected 0",
                  response.transferred_bytes);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK && response.request_to_first_byte_ms <= 0) {
        set_error(test, "latency request produced invalid timing %.3f ms",
                  response.request_to_first_byte_ms);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK) {
        memset(sample, 0, sizeof(*sample));
        sample->ts = ended;
        sample->started_at = started;
        sample->ended_at = ended;
        sample->rtt_ms = response.request_to_first_byte_ms;
        snprintf(sample->condition, sizeof(sample->condition), "%s", condition);
        snprintf(sample->timing_source, sizeof(sample->timing_source), "%s", "libcurl");
    }
    http_response_free(&response);
    return error;
}

static int measure_download_with_session(speedtest_t *test, http_session_t *session,
                                         const char *profile, int64_t bytes, int run,
                                         const http_activity_t *activity,
                                         throughput_sample_t *sample)
{
    if (bytes < 0 || bytes > test->results.meta.max_transfer_bytes) {
    ns_progress("download measurement");
        set_error(test, "download size %" PRId64 " exceeds negotiated maximum %" PRId64,
                  bytes, test->results.meta.max_transfer_bytes);
        return ERR_PROTOCOL;
    }
    char path[768];
    make_measurement_path(path, sizeof(path), "/__down", bytes, profile, run, "");
    http_response_t response;
    int error = http_measure_download(session, path, bytes, activity, &response);
    if (error != ERR_OK) {
        set_error(test, "download request failed: %s", http_session_error(session));
        http_response_free(&response);
        return error;
    }
    error = validate_status(test, session, &response, 200, "download request");
    if (error == ERR_OK && !content_type_prefix(response.content_type, "application/octet-stream")) {
        set_error(test, "download returned unexpected content type %s", response.content_type);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK && response.content_length >= 0 && response.content_length != bytes) {
        set_error(test, "download Content-Length was %" PRId64 "; expected %" PRId64,
                  response.content_length, bytes);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK && response.transferred_bytes != bytes) {
        set_error(test, "download returned %" PRId64 " bytes; expected %" PRId64,
                  response.transferred_bytes, bytes);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK && response.body_duration_ms <= 0) {
        set_error(test, "download produced invalid body duration %.3f ms",
                  response.body_duration_ms);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK) {
        memset(sample, 0, sizeof(*sample));
        sample->ts = timing_now_ms();
        snprintf(sample->direction, sizeof(sample->direction), "%s", "download");
        sample->size_bytes = bytes;
        sample->duration_ms = response.body_duration_ms;
        sample->mbps = bytes_to_mbps(bytes, response.body_duration_ms);
        snprintf(sample->profile, sizeof(sample->profile), "%s", profile);
        sample->run_index = run;
        snprintf(sample->timing_source, sizeof(sample->timing_source), "%s", "libcurl-body");
    }
    http_response_free(&response);
    return error;
}

static int parse_upload_receipt(const char *body, int64_t expected_bytes,
                                int64_t *accepted_bytes, int64_t *duration_ns)
{
    ns_progress("upload measurement");
    json_value_t *root = json_parse(body ? body : "");
    if (!root) {
        return -1;
    }
    bool ok = json_get_bool(root, "ok", false);
    double accepted = json_get_number(root, "acceptedBytes", -1);
    double duration = json_get_number(root, "serverDurationNs", -1);
    json_free(root);
    if (!ok || accepted < 0 || duration < 0 || (int64_t)accepted != expected_bytes) {
        return -1;
    }
    *accepted_bytes = (int64_t)accepted;
    *duration_ns = (int64_t)duration;
    return 0;
}

static int measure_upload_with_session(speedtest_t *test, http_session_t *session,
                                       const char *profile, int64_t bytes, int run,
                                       const http_activity_t *activity,
                                       throughput_sample_t *sample)
{
    if (bytes < 0 || bytes > test->results.meta.max_transfer_bytes) {
    ns_progress("upload measurement");
        set_error(test, "upload size %" PRId64 " exceeds negotiated maximum %" PRId64,
                  bytes, test->results.meta.max_transfer_bytes);
        return ERR_PROTOCOL;
    }
    char path[768];
    make_measurement_path(path, sizeof(path), "/__up", bytes, profile, run, "");
    http_response_t response;
    int error = http_measure_upload(session, path, bytes, activity, RECEIPT_BODY_LIMIT, &response);
    if (error != ERR_OK) {
        set_error(test, "upload request failed: %s", http_session_error(session));
        http_response_free(&response);
        return error;
    }
    error = validate_status(test, session, &response, 200, "upload request");
    if (error == ERR_OK && !content_type_prefix(response.content_type, "application/json")) {
        set_error(test, "upload receipt returned unexpected content type %s", response.content_type);
        error = ERR_PROTOCOL;
    }
    int64_t accepted = 0;
    int64_t duration_ns = 0;
    if (error == ERR_OK && parse_upload_receipt(response.body, bytes, &accepted, &duration_ns) != 0) {
        set_error(test, "upload receipt did not verify the exact %" PRId64 "-byte body", bytes);
        error = ERR_PROTOCOL;
    }
    double duration_ms = (double)duration_ns / 1000000.0;
    const char *timing_source = "server-receipt";
    if (error == ERR_OK && duration_ms <= 0) {
        duration_ms = response.body_duration_ms;
        timing_source = "libcurl-upload";
    }
    if (error == ERR_OK && duration_ms <= 0) {
        set_error(test, "upload produced invalid duration %.3f ms", duration_ms);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK) {
        memset(sample, 0, sizeof(*sample));
        sample->ts = timing_now_ms();
        snprintf(sample->direction, sizeof(sample->direction), "%s", "upload");
        sample->size_bytes = accepted;
        sample->duration_ms = duration_ms;
        sample->mbps = bytes_to_mbps(accepted, duration_ms);
        snprintf(sample->profile, sizeof(sample->profile), "%s", profile);
        sample->run_index = run;
        snprintf(sample->timing_source, sizeof(sample->timing_source), "%s", timing_source);
    }
    http_response_free(&response);
    return error;
}

static void *latency_worker_main(void *opaque)
{
    ns_progress("latency probes");
    latency_task_t *task = opaque;
    http_session_t session;
    http_session_init(&session, &task->test->http);
    if (!session.easy) {
        task->error = ERR_MEMORY;
        return NULL;
    }
    task->error = measure_latency_with_session(task->test, &session, task->condition,
                                               task->sequence, &task->sample);
    http_session_cleanup(&session);
    return NULL;
}

static int run_parallel_latency(speedtest_t *test, const char *condition, int start_sequence,
                                int count, latency_sample_t *samples, int *sample_count)
{
    ns_progress("latency probes");
    latency_task_t tasks[LATENCY_BATCH_SIZE];
    pthread_t threads[LATENCY_BATCH_SIZE];
    bool created[LATENCY_BATCH_SIZE];
    memset(tasks, 0, sizeof(tasks));
    memset(created, 0, sizeof(created));
    for (int index = 0; index < count; index++) {
        tasks[index].test = test;
        snprintf(tasks[index].condition, sizeof(tasks[index].condition), "%s", condition);
        tasks[index].sequence = start_sequence + index;
        if (pthread_create(&threads[index], NULL, latency_worker_main, &tasks[index]) != 0) {
            tasks[index].error = ERR_NETWORK;
            continue;
        }
        created[index] = true;
    }
    for (int index = 0; index < count; index++) {
        if (created[index]) {
            pthread_join(threads[index], NULL);
        }
        if (created[index] && tasks[index].error == ERR_OK) {
            samples[(*sample_count)++] = tasks[index].sample;
        }
    }
    return ERR_OK;
}

static double quick_bandwidth_estimate(speedtest_t *test)
{
    if (test->results.meta.max_transfer_bytes < BASELINE_SMALL_BYTES) {
        return 0;
    }
    http_session_t session;
    http_session_init(&session, &test->http);
    if (!session.easy) {
        return 0;
    }
    throughput_sample_t sample;
    int error = measure_download_with_session(test, &session, "estimate",
                                              BASELINE_SMALL_BYTES, 0, NULL, &sample);
    http_session_cleanup(&session);
    return error == ERR_OK ? sample.mbps : 0;
}

int speedtest_latency(speedtest_t *test, const char *condition, int count)
{
    if (test->config->quick) {
    ns_progress("latency probes");
        count = LATENCY_PROBES_QUICK;
    }
    latency_sample_t samples[LATENCY_PROBES_FULL];
    int sample_count = 0;
    int initial = count < INITIAL_PROBES ? count : INITIAL_PROBES;
    http_session_t session;
    http_session_init(&session, &test->http);
    if (!session.easy) {
        set_error(test, "failed to initialize HTTP latency session");
        return ERR_MEMORY;
    }
    for (int index = 0; index < initial; index++) {
        latency_sample_t sample;
        if (measure_latency_with_session(test, &session, condition, index, &sample) == ERR_OK) {
            samples[sample_count++] = sample;
            if (test->on_progress) {
                test->on_progress("latency", index + 1, count, sample.rtt_ms);
            }
        }
    }
    http_session_cleanup(&session);
    if (sample_count == 0) {
        set_error(test, "%s latency test produced no valid samples", condition);
        return ERR_INCOMPLETE;
    }
    double rtts[LATENCY_PROBES_FULL];
    for (int index = 0; index < sample_count; index++) {
        rtts[index] = samples[index].rtt_ms;
    }
    double median = stats_percentile(rtts, (size_t)sample_count, 50);
    bool parallel = median < LOW_LATENCY_THRESHOLD_MS;
    if (median >= LOW_LATENCY_THRESHOLD_MS && median < HIGH_LATENCY_THRESHOLD_MS) {
        parallel = true;
    } else if (median >= HIGH_LATENCY_THRESHOLD_MS) {
        parallel = quick_bandwidth_estimate(test) >= MIN_BANDWIDTH_FOR_PARALLEL;
    }

    if (parallel) {
        int batch_size = LATENCY_BATCH_SIZE;
        if (test->results.meta.max_concurrent_transfers_per_client < batch_size) {
            batch_size = test->results.meta.max_concurrent_transfers_per_client;
        }
        if (batch_size < 1) {
            batch_size = 1;
        }
        while (sample_count < count && !test_expired(test)) {
            int batch = count - sample_count;
            if (batch > batch_size) {
                batch = batch_size;
            }
            int start_sequence = sample_count;
            run_parallel_latency(test, condition, start_sequence, batch, samples, &sample_count);
            if (test->on_progress) {
                test->on_progress("latency", sample_count, count, median);
            }
        }
    } else {
        http_session_init(&session, &test->http);
        for (int sequence = sample_count; sequence < count && !test_expired(test); sequence++) {
            latency_sample_t sample;
            if (measure_latency_with_session(test, &session, condition, sequence, &sample) == ERR_OK) {
                samples[sample_count++] = sample;
                if (test->on_progress) {
                    test->on_progress("latency", sample_count, count, sample.rtt_ms);
                }
            }
        }
        http_session_cleanup(&session);
    }
    if (sample_count < required_successful_runs(count)) {
        set_error(test, "%s latency test produced %d/%d valid samples", condition, sample_count, count);
        return ERR_INCOMPLETE;
    }
    for (int index = 0; index < sample_count; index++) {
        int error = append_latency(test, &samples[index]);
        if (error != ERR_OK) {
            return error;
        }
    }
    return ERR_OK;
}

static int run_baseline(speedtest_t *test, const char *direction)
{
    ns_progress("calibration transfer");
    const int64_t sizes[] = {BASELINE_SMALL_BYTES, BASELINE_LARGE_BYTES};
    const char *names[] = {"100kB", "1MB"};
    if (test->results.meta.max_transfer_bytes < BASELINE_LARGE_BYTES) {
        set_error(test, "server transfer limit %" PRId64 " is below the 1MB %s baseline",
                  test->results.meta.max_transfer_bytes, direction);
        return ERR_PROTOCOL;
    }
    http_session_t session;
    http_session_init(&session, &test->http);
    if (!session.easy) {
        return ERR_MEMORY;
    }
    int completed = 0;
    int total = 2 * BASELINE_RUNS;
    for (int profile = 0; profile < 2; profile++) {
        throughput_sample_t valid[BASELINE_RUNS];
        int valid_count = 0;
        for (int run = 0; run < BASELINE_RUNS && !test_expired(test); run++) {
            throughput_sample_t sample;
            int error = strcmp(direction, "download") == 0
                            ? measure_download_with_session(test, &session, names[profile], sizes[profile], run, NULL, &sample)
                            : measure_upload_with_session(test, &session, names[profile], sizes[profile], run, NULL, &sample);
            if (error != ERR_OK) {
                continue;
            }
            snprintf(sample.sample_kind, sizeof(sample.sample_kind), "%s", "baseline");
            valid[valid_count++] = sample;
            completed++;
            if (test->on_progress) {
                test->on_progress(direction, completed, total, sample.mbps);
            }
        }
        if (valid_count < required_successful_runs(BASELINE_RUNS)) {
            http_session_cleanup(&session);
            set_error(test, "%s baseline %s produced %d/%d valid samples",
                      direction, names[profile], valid_count, BASELINE_RUNS);
            return ERR_INCOMPLETE;
        }
        for (int index = 0; index < valid_count; index++) {
            int error = append_throughput(test, &valid[index]);
            if (error != ERR_OK) {
                http_session_cleanup(&session);
                return error;
            }
        }
    }
    http_session_cleanup(&session);
    return ERR_OK;
}

static double median_baseline_speed(const speedtest_t *test, const char *direction)
{
    ns_progress("calibration transfer");
    double values[BASELINE_RUNS];
    size_t count = 0;
    for (int index = 0; index < test->results.throughput_count; index++) {
        const throughput_sample_t *sample = &test->results.throughput_samples[index];
        if (strcmp(sample->direction, direction) == 0 && strcmp(sample->profile, "1MB") == 0 &&
            count < BASELINE_RUNS) {
            values[count++] = sample->mbps;
        }
    }
    return count ? stats_percentile(values, count, 50) : 10;
}

static window_plan_t select_window_plan(double estimated_mbps, int64_t max_bytes,
                                        int max_concurrency, bool quick)
{
    if (estimated_mbps <= 0 || !isfinite(estimated_mbps)) {
    ns_progress("sustained measurement window");
        estimated_mbps = 10;
    }
    int concurrency = 1;
    if (estimated_mbps >= 10000) {
        concurrency = 16;
    } else if (estimated_mbps >= 2000) {
        concurrency = 8;
    } else if (estimated_mbps >= 500) {
        concurrency = 4;
    } else if (estimated_mbps >= 100) {
        concurrency = 2;
    }
    /* Match the Go client: size the bounded chunk from the unconstrained plan,
     * then reserve one advertised server slot for loaded-latency probes. */
    double target = estimated_mbps * 1000000.0 / 8.0 *
                    ((double)WINDOW_TARGET_MS / 1000.0) / (double)concurrency;
    int64_t chunk = (int64_t)ceil(target / 65536.0) * 65536LL;
    if (chunk < MIN_WINDOW_CHUNK_BYTES) {
        chunk = MIN_WINDOW_CHUNK_BYTES;
    }
    if (chunk > MAX_WINDOW_CHUNK_BYTES) {
        chunk = MAX_WINDOW_CHUNK_BYTES;
    }
    if (max_bytes > 0 && chunk > max_bytes) {
        chunk = max_bytes;
    }
    int ceiling = max_concurrency - 1;
    if (ceiling < 1) {
        ceiling = 1;
    }
    if (concurrency > ceiling) {
        concurrency = ceiling;
    }
    window_plan_t plan = {
        .chunk_bytes = chunk,
        .concurrency = concurrency,
        .window_duration_ms = quick ? WINDOW_DURATION_QUICK_MS : WINDOW_DURATION_FULL_MS,
        .windows = quick ? WINDOW_COUNT_QUICK : WINDOW_COUNT_FULL,
        .loaded_window = quick ? 0 : 1,
        .loaded_probe_count = quick ? LOADED_PROBES_QUICK : LOADED_PROBES_FULL,
    };
    return plan;
}

static void load_activity_init(load_activity_t *activity)
{
    memset(activity, 0, sizeof(*activity));
    pthread_mutex_init(&activity->mutex, NULL);
}

static void load_activity_destroy(load_activity_t *activity)
{
    pthread_mutex_destroy(&activity->mutex);
}

static void load_begin(void *opaque)
{
    load_activity_t *activity = opaque;
    pthread_mutex_lock(&activity->mutex);
    activity->active++;
    pthread_mutex_unlock(&activity->mutex);
}

static void load_end(void *opaque)
{
    load_activity_t *activity = opaque;
    pthread_mutex_lock(&activity->mutex);
    if (activity->active > 0) {
        activity->active--;
        if (activity->active == 0) {
            activity->gap_generation++;
        }
    }
    pthread_mutex_unlock(&activity->mutex);
}

static load_snapshot_t load_snapshot(load_activity_t *activity)
{
    pthread_mutex_lock(&activity->mutex);
    load_snapshot_t snapshot = {.active = activity->active,
                                .gap_generation = activity->gap_generation};
    pthread_mutex_unlock(&activity->mutex);
    return snapshot;
}

static bool wait_for_load(load_activity_t *activity, window_control_t *control, int timeout_ms)
{
    int64_t deadline = timing_monotonic_ms() + timeout_ms;
    while (timing_monotonic_ms() < deadline && !atomic_load(&control->stop)) {
        if (load_snapshot(activity).active > 0) {
            return true;
        }
        timing_sleep_ms(2);
    }
    return false;
}

static void aggregate_success(window_aggregate_t *aggregate, int64_t bytes)
{
    pthread_mutex_lock(&aggregate->mutex);
    aggregate->bytes += bytes;
    aggregate->requests++;
    pthread_mutex_unlock(&aggregate->mutex);
}

static void aggregate_failure(window_aggregate_t *aggregate, const char *error)
{
    pthread_mutex_lock(&aggregate->mutex);
    snprintf(aggregate->last_error, sizeof(aggregate->last_error), "%s", error ? error : "request failed");
    pthread_mutex_unlock(&aggregate->mutex);
}

static void *window_worker_main(void *opaque)
{
    ns_progress("sustained measurement window");
    window_worker_t *worker = opaque;
    pthread_mutex_lock(&worker->control->mutex);
    while (!worker->control->started) {
        pthread_cond_wait(&worker->control->condition, &worker->control->mutex);
    }
    pthread_mutex_unlock(&worker->control->mutex);

    http_session_t session;
    http_session_init(&session, &worker->test->http);
    if (!session.easy) {
        aggregate_failure(worker->aggregate, "failed to initialize worker HTTP session");
        return NULL;
    }
    http_activity_t activity = {.begin = load_begin, .end = load_end, .opaque = worker->activity};
    int request_index = 0;
    while (!atomic_load(&worker->control->stop) && !worker->test->aborted &&
           timing_before_deadline(&worker->test->deadline)) {
        char profile[24];
        snprintf(profile, sizeof(profile), "window-%d", worker->window_index + 1);
        int run = worker->worker_index * 1000000 + request_index++;
        throughput_sample_t sample;
        int error = strcmp(worker->direction, "download") == 0
                        ? measure_download_with_session(worker->test, &session, profile,
                                                        worker->plan.chunk_bytes, run,
                                                        &activity, &sample)
                        : measure_upload_with_session(worker->test, &session, profile,
                                                      worker->plan.chunk_bytes, run,
                                                      &activity, &sample);
        if (error == ERR_OK) {
            aggregate_success(worker->aggregate, sample.size_bytes);
        } else {
            char worker_error[MAX_ERROR_LEN] = {0};
            copy_error(worker->test, worker_error, sizeof(worker_error));
            aggregate_failure(worker->aggregate, worker_error);
            if (!atomic_load(&worker->control->stop)) {
                timing_sleep_ms(10);
            }
        }
    }
    http_session_cleanup(&session);
    return NULL;
}

static void *loaded_probe_main(void *opaque)
{
    loaded_probe_task_t *task = opaque;
    http_session_t session;
    http_session_init(&session, &task->test->http);
    if (!session.easy) {
        task->error = ERR_MEMORY;
        snprintf(task->error_message, sizeof(task->error_message), "%s", "failed to initialize loaded-latency session");
        return NULL;
    }
    http_session_set_cancel(&session, &task->control->stop);
    int max_attempts = task->count * 5;
    for (int attempt = 0; attempt < max_attempts && task->sample_count < task->count; attempt++) {
        if (atomic_load(&task->control->stop) || task->test->aborted) {
            break;
        }
        if (!wait_for_load(task->activity, task->control, 2000)) {
            snprintf(task->error_message, sizeof(task->error_message), "%s", "timed out waiting for sustained load");
            continue;
        }
        load_snapshot_t before = load_snapshot(task->activity);
        if (before.active <= 0) {
            continue;
        }
        latency_sample_t sample;
        int error = measure_latency_with_session(task->test, &session, task->condition, attempt, &sample);
        if (error != ERR_OK) {
            copy_error(task->test, task->error_message, sizeof(task->error_message));
            continue;
        }
        load_snapshot_t after = load_snapshot(task->activity);
        bool overlap = before.active > 0 && after.active > 0 &&
                       before.gap_generation == after.gap_generation;
        if (!overlap) {
            snprintf(task->error_message, sizeof(task->error_message), "%s",
                     "probe did not remain inside a continuous load interval");
            continue;
        }
        sample.load_overlapped = true;
        sample.load_tracking_accurate = true;
        task->samples[task->sample_count++] = sample;
        if (task->test->on_progress) {
            task->test->on_progress("loaded-latency", task->sample_count, task->count, sample.rtt_ms);
        }
        timing_sleep_ms(25);
    }
    http_session_cleanup(&session);
    if (task->sample_count < required_successful_runs(task->count)) {
        task->error = ERR_INCOMPLETE;
        if (!task->error_message[0]) {
            snprintf(task->error_message, sizeof(task->error_message), "%s",
                     "insufficient continuously-overlapped probes");
        }
    }
    return NULL;
}

static int run_window(speedtest_t *test, const char *direction, window_plan_t plan,
                      int window_index, bool with_loaded_latency,
                      throughput_sample_t *window_sample,
                      latency_sample_t *loaded_samples, int *loaded_count)
{
    ns_progress("sustained measurement window");
    load_activity_t activity;
    load_activity_init(&activity);
    window_control_t control;
    memset(&control, 0, sizeof(control));
    pthread_mutex_init(&control.mutex, NULL);
    pthread_cond_init(&control.condition, NULL);
    atomic_init(&control.stop, false);
    window_aggregate_t aggregate;
    memset(&aggregate, 0, sizeof(aggregate));
    pthread_mutex_init(&aggregate.mutex, NULL);

    pthread_t workers[16];
    window_worker_t worker_args[16];
    int worker_count = 0;
    for (int index = 0; index < plan.concurrency; index++) {
        worker_args[index] = (window_worker_t){
            .test = test,
            .direction = direction,
            .plan = plan,
            .window_index = window_index,
            .worker_index = index,
            .activity = &activity,
            .control = &control,
            .aggregate = &aggregate,
        };
        if (pthread_create(&workers[index], NULL, window_worker_main, &worker_args[index]) != 0) {
            break;
        }
        worker_count++;
    }
    if (worker_count != plan.concurrency) {
        pthread_mutex_lock(&control.mutex);
        control.started = true;
        pthread_cond_broadcast(&control.condition);
        pthread_mutex_unlock(&control.mutex);
        atomic_store(&control.stop, true);
        for (int index = 0; index < worker_count; index++) {
            pthread_join(workers[index], NULL);
        }
        set_error(test, "started %d/%d %s window workers", worker_count, plan.concurrency, direction);
        pthread_mutex_destroy(&aggregate.mutex);
        pthread_cond_destroy(&control.condition);
        pthread_mutex_destroy(&control.mutex);
        load_activity_destroy(&activity);
        return ERR_NETWORK;
    }

    struct timespec window_start;
    timing_now(&window_start);
    pthread_mutex_lock(&control.mutex);
    control.started = true;
    pthread_cond_broadcast(&control.condition);
    pthread_mutex_unlock(&control.mutex);

    loaded_probe_task_t probe_task;
    pthread_t probe_thread;
    bool probe_started = false;
    if (with_loaded_latency) {
        memset(&probe_task, 0, sizeof(probe_task));
        probe_task.test = test;
        probe_task.condition = direction;
        probe_task.count = plan.loaded_probe_count;
        probe_task.activity = &activity;
        probe_task.control = &control;
        probe_started = pthread_create(&probe_thread, NULL, loaded_probe_main, &probe_task) == 0;
        if (!probe_started) {
            atomic_store(&control.stop, true);
        }
    }

    int64_t stop_at = timing_monotonic_ms() + plan.window_duration_ms;
    while (timing_monotonic_ms() < stop_at && !test->aborted &&
           timing_before_deadline(&test->deadline)) {
        timing_sleep_ms(2);
    }
    atomic_store(&control.stop, true);

    if (probe_started) {
        pthread_join(probe_thread, NULL);
    }
    for (int index = 0; index < worker_count; index++) {
        pthread_join(workers[index], NULL);
    }
    struct timespec window_end;
    timing_now(&window_end);

    pthread_mutex_lock(&aggregate.mutex);
    int64_t bytes = aggregate.bytes;
    int requests = aggregate.requests;
    char last_error[MAX_ERROR_LEN];
    snprintf(last_error, sizeof(last_error), "%s", aggregate.last_error);
    pthread_mutex_unlock(&aggregate.mutex);

    int error = ERR_OK;
    if (bytes <= 0 || requests == 0) {
        set_error(test, "%s window %d completed no verified requests%s%s",
                  direction, window_index + 1, last_error[0] ? ": " : "", last_error);
        error = ERR_INCOMPLETE;
    } else if (with_loaded_latency && (!probe_started || probe_task.error != ERR_OK)) {
        set_error(test, "%s loaded-latency window %d: %s", direction, window_index + 1,
                  probe_started ? probe_task.error_message : "failed to start probe worker");
        error = ERR_INCOMPLETE;
    } else {
        double duration_ms = timing_diff_ms(&window_start, &window_end);
        memset(window_sample, 0, sizeof(*window_sample));
        window_sample->ts = timing_now_ms();
        snprintf(window_sample->direction, sizeof(window_sample->direction), "%s", direction);
        window_sample->size_bytes = bytes;
        window_sample->duration_ms = duration_ms;
        window_sample->mbps = bytes_to_mbps(bytes, duration_ms);
        snprintf(window_sample->profile, sizeof(window_sample->profile), "%s", "window");
        window_sample->run_index = window_index;
        snprintf(window_sample->sample_kind, sizeof(window_sample->sample_kind), "%s", "window");
        window_sample->window_index = window_index;
        window_sample->has_window_index = true;
        window_sample->concurrency = plan.concurrency;
        window_sample->chunk_bytes = plan.chunk_bytes;
        window_sample->request_count = requests;
        snprintf(window_sample->timing_source, sizeof(window_sample->timing_source), "%s",
                 "aggregate-wall-clock");
        if (with_loaded_latency) {
            *loaded_count = probe_task.sample_count;
            for (int index = 0; index < probe_task.sample_count; index++) {
                loaded_samples[index] = probe_task.samples[index];
            }
        }
    }

    pthread_mutex_destroy(&aggregate.mutex);
    pthread_cond_destroy(&control.condition);
    pthread_mutex_destroy(&control.mutex);
    load_activity_destroy(&activity);
    return error;
}

static int run_direction(speedtest_t *test, const char *direction)
{
    int error = run_baseline(test, direction);
    if (error != ERR_OK) {
        return error;
    }
    window_plan_t plan = select_window_plan(median_baseline_speed(test, direction),
                                            test->results.meta.max_transfer_bytes,
                                            test->results.meta.max_concurrent_transfers_per_client,
                                            test->config->quick);
    for (int window = 0; window < plan.windows; window++) {
        throughput_sample_t sample;
        latency_sample_t loaded[LOADED_PROBES_FULL];
        int loaded_count = 0;
        error = run_window(test, direction, plan, window, window == plan.loaded_window,
                           &sample, loaded, &loaded_count);
        if (error != ERR_OK) {
            return error;
        }
        if ((error = append_throughput(test, &sample)) != ERR_OK) {
            return error;
        }
        for (int index = 0; index < loaded_count; index++) {
            if ((error = append_latency(test, &loaded[index])) != ERR_OK) {
                return error;
            }
        }
        if (test->on_progress) {
            test->on_progress(direction, window + 1, plan.windows, sample.mbps);
        }
    }
    return ERR_OK;
}

int speedtest_download(speedtest_t *test)
{
    ns_progress("download measurement");
    return run_direction(test, "download");
}

int speedtest_upload(speedtest_t *test)
{
    ns_progress("upload measurement");
    return run_direction(test, "upload");
}

int speedtest_fetch_meta(speedtest_t *test)
{
    http_session_t session;
    http_session_init(&session, &test->http);
    if (!session.easy) {
        set_error(test, "failed to initialize metadata HTTP session");
        return ERR_MEMORY;
    }
    http_response_t response;
    int error = http_get_json(&session, "/meta", META_BODY_LIMIT, &response);
    if (error != ERR_OK) {
        set_error(test, "metadata request failed: %s", http_session_error(&session));
    } else if ((error = validate_status(test, &session, &response, 200, "metadata request")) == ERR_OK &&
               !content_type_prefix(response.content_type, "application/json")) {
        set_error(test, "metadata returned unexpected content type %s", response.content_type);
        error = ERR_PROTOCOL;
    }
    if (error == ERR_OK && meta_from_json(response.body, &test->results.meta) != 0) {
        set_error(test, "failed to parse metadata response");
        error = ERR_PARSE;
    }
    http_response_free(&response);
    http_session_cleanup(&session);
    if (error != ERR_OK) {
        return error;
    }
    const meta_t *meta = &test->results.meta;
    if (meta->measurement_protocol_version < NETSPEED_MEASUREMENT_PROTOCOL_VERSION) {
        set_error(test, "server measurement protocol %d is too old; need version %d",
                  meta->measurement_protocol_version, NETSPEED_MEASUREMENT_PROTOCOL_VERSION);
        return ERR_PROTOCOL;
    }
    if (meta->max_transfer_bytes < BASELINE_LARGE_BYTES) {
        set_error(test, "server maximum transfer size %" PRId64 " is below the 1MB baseline",
                  meta->max_transfer_bytes);
        return ERR_PROTOCOL;
    }
    if (meta->max_concurrent_transfers_per_client < 2) {
        set_error(test, "server per-client transfer limit %d is too low; need at least 2",
                  meta->max_concurrent_transfers_per_client);
        return ERR_PROTOCOL;
    }
    if (!test->config->download_only &&
        meta->upload_receipt_version < NETSPEED_UPLOAD_RECEIPT_VERSION) {
        set_error(test, "server does not support verified upload receipts version %d",
                  NETSPEED_UPLOAD_RECEIPT_VERSION);
        return ERR_PROTOCOL;
    }
    return ERR_OK;
}

int speedtest_packet_loss(speedtest_t *test)
{
    test->results.packet_loss_present = true;
    packet_loss_config_t config = {
        .http = &test->http,
        .server_frame_version = test->results.meta.packet_loss_frame_version,
        .deadline = test->deadline,
        .aborted = &test->aborted,
        .progress = test->on_progress,
    };
    char error[MAX_ERROR_LEN] = {0};
    int result = packet_loss_run(&config, &test->results.packet_loss, error, sizeof(error));
    if (result != ERR_OK) {
        memset(&test->results.packet_loss, 0, sizeof(test->results.packet_loss));
        test->results.packet_loss.unavailable = true;
        snprintf(test->results.packet_loss.reason, sizeof(test->results.packet_loss.reason),
                 "%.255s", error[0] ? error : "packet-loss test failed");
        return ERR_OK;
    }
    return ERR_OK;
}

void speedtest_init(speedtest_t *test, config_t *config)
{
    memset(test, 0, sizeof(*test));
    pthread_mutex_init(&test->error_mutex, NULL);
    test->config = config;
    struct timespec now;
    timing_now(&now);
    int64_t timeout_ms = config->timeout_ms > 0 ? config->timeout_ms : DEFAULT_TEST_TIMEOUT_MS;
    test->deadline = now;
    test->deadline.tv_sec += timeout_ms / 1000;
    test->deadline.tv_nsec += (long)(timeout_ms % 1000) * 1000000L;
    if (test->deadline.tv_nsec >= 1000000000L) {
        test->deadline.tv_sec++;
        test->deadline.tv_nsec -= 1000000000L;
    }
    long request_timeout = timeout_ms > 120000 ? 120000L : (long)timeout_ms;
    if (request_timeout < 1000) {
        request_timeout = 1000;
    }
    http_client_init(&test->http, config->server_url, config->access_token,
                     request_timeout, &test->aborted);
}

void speedtest_set_progress(speedtest_t *test, progress_fn progress)
{
    test->on_progress = progress;
}

void speedtest_abort(speedtest_t *test)
{
    if (test) {
        test->aborted = 1;
    }
}

void speedtest_cleanup(speedtest_t *test)
{
    if (test) {
        pthread_mutex_destroy(&test->error_mutex);
    }
}

const results_t *speedtest_results(const speedtest_t *test)
{
    return &test->results;
}

const char *speedtest_error(const speedtest_t *test)
{
    return test && test->last_error[0] ? test->last_error : "speed test failed";
}

int speedtest_run(speedtest_t *test)
{
    results_t *results = &test->results;
    memset(results, 0, sizeof(*results));
    snprintf(results->server_url, sizeof(results->server_url), "%s", test->config->server_url);
    results->timestamp = timing_now_ms();
    results->start_time = results->timestamp;
    timing_format_rfc3339_ms(results->start_time, results->start_time_rfc3339,
                             sizeof(results->start_time_rfc3339));

    int error = speedtest_fetch_meta(test);
    if (error != ERR_OK) {
        return error;
    }
    error = speedtest_latency(test, "unloaded", LATENCY_PROBES_FULL);
    if (error != ERR_OK) {
        return error;
    }
    if (!test->config->upload_only) {
        error = speedtest_download(test);
        if (error != ERR_OK) {
            return error;
        }
    }
    if (!test->config->download_only) {
        error = speedtest_upload(test);
        if (error != ERR_OK) {
            return error;
        }
    }
    if (!test->config->skip_packet_loss) {
        (void)speedtest_packet_loss(test);
    }
    calculate_summary(results);
    calculate_quality(results);
    results->end_time = timing_now_ms();
    timing_format_rfc3339_ms(results->end_time, results->end_time_rfc3339,
                             sizeof(results->end_time_rfc3339));
    assess_test_confidence(results, test->config);
    return ERR_OK;
}
