#include "timing.h"

#include <stdio.h>

void timing_now(struct timespec *ts)
{
    clock_gettime(CLOCK_MONOTONIC, ts);
}

int64_t timing_now_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

int64_t timing_monotonic_ms(void)
{
    struct timespec ts;
    timing_now(&ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

double timing_diff_ms(const struct timespec *start, const struct timespec *end)
{
    return (double)(end->tv_sec - start->tv_sec) * 1000.0 +
           (double)(end->tv_nsec - start->tv_nsec) / 1000000.0;
}

double timing_diff_sec(const struct timespec *start, const struct timespec *end)
{
    return timing_diff_ms(start, end) / 1000.0;
}

void timing_sleep_ms(int ms)
{
    if (ms <= 0) {
        return;
    }
    struct timespec requested = {.tv_sec = ms / 1000, .tv_nsec = (long)(ms % 1000) * 1000000L};
    while (nanosleep(&requested, &requested) != 0) {
        continue;
    }
}

void timing_format_rfc3339_ms(int64_t unix_ms, char *buffer, size_t buffer_len)
{
    time_t seconds = (time_t)(unix_ms / 1000);
    int millis = (int)(unix_ms % 1000);
    if (millis < 0) {
        millis += 1000;
        seconds--;
    }
    struct tm tm_value;
    if (gmtime_r(&seconds, &tm_value) == NULL) {
        if (buffer_len > 0) {
            buffer[0] = '\0';
        }
        return;
    }
    char prefix[32];
    strftime(prefix, sizeof(prefix), "%Y-%m-%dT%H:%M:%S", &tm_value);
    snprintf(buffer, buffer_len, "%s.%03dZ", prefix, millis);
}

bool timing_before_deadline(const struct timespec *deadline)
{
    struct timespec now;
    timing_now(&now);
    return now.tv_sec < deadline->tv_sec ||
           (now.tv_sec == deadline->tv_sec && now.tv_nsec < deadline->tv_nsec);
}

int64_t timing_remaining_ms(const struct timespec *deadline)
{
    struct timespec now;
    timing_now(&now);
    double remaining = timing_diff_ms(&now, deadline);
    return remaining > 0 ? (int64_t)remaining : 0;
}
