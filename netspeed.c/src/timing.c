/*
 * timing.c - Precise timing functions
 */

#include "timing.h"
#include <string.h>

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

double timing_diff_ms(const struct timespec *start, const struct timespec *end)
{
    double sec = (double)(end->tv_sec - start->tv_sec);
    double nsec = (double)(end->tv_nsec - start->tv_nsec);
    return sec * 1000.0 + nsec / 1000000.0;
}

double timing_diff_sec(const struct timespec *start, const struct timespec *end)
{
    double sec = (double)(end->tv_sec - start->tv_sec);
    double nsec = (double)(end->tv_nsec - start->tv_nsec);
    return sec + nsec / 1000000000.0;
}

void timing_info_init(timing_info_t *t)
{
    memset(t, 0, sizeof(*t));
    t->wrote_request_set = false;
    t->got_first_byte_set = false;
}

void timing_mark_wrote_request(timing_info_t *t)
{
    timing_now(&t->wrote_request);
    t->wrote_request_set = true;
}

void timing_mark_got_first_byte(timing_info_t *t)
{
    timing_now(&t->got_first_byte);
    t->got_first_byte_set = true;
}

void timing_mark_body_done(timing_info_t *t)
{
    timing_now(&t->body_done);
}

double timing_get_rtt_ms(const timing_info_t *t)
{
    if (!t->wrote_request_set || !t->got_first_byte_set) {
        return -1.0;
    }
    return timing_diff_ms(&t->wrote_request, &t->got_first_byte);
}

double timing_get_body_time_ms(const timing_info_t *t)
{
    if (!t->got_first_byte_set) {
        return -1.0;
    }
    return timing_diff_ms(&t->got_first_byte, &t->body_done);
}

void timing_sleep_ms(int ms)
{
    struct timespec ts;
    ts.tv_sec = ms / 1000;
    ts.tv_nsec = (ms % 1000) * 1000000;
    nanosleep(&ts, NULL);
}
