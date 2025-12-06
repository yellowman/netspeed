/*
 * timing.h - Precise timing functions
 *
 * Uses clock_gettime(CLOCK_MONOTONIC) for accurate measurements.
 */

#ifndef NETSPEED_TIMING_H
#define NETSPEED_TIMING_H

#include "types.h"
#include <time.h>

/*
 * Get current monotonic time.
 */
void timing_now(struct timespec *ts);

/*
 * Get current wall-clock time in milliseconds since epoch.
 */
int64_t timing_now_ms(void);

/*
 * Calculate duration between two timespecs in milliseconds.
 */
double timing_diff_ms(const struct timespec *start, const struct timespec *end);

/*
 * Calculate duration between two timespecs in seconds.
 */
double timing_diff_sec(const struct timespec *start, const struct timespec *end);

/*
 * Initialize timing info structure.
 */
void timing_info_init(timing_info_t *t);

/*
 * Record "wrote request" timestamp.
 */
void timing_mark_wrote_request(timing_info_t *t);

/*
 * Record "got first byte" timestamp.
 */
void timing_mark_got_first_byte(timing_info_t *t);

/*
 * Record "body done" timestamp.
 */
void timing_mark_body_done(timing_info_t *t);

/*
 * Get RTT (request to first byte) in milliseconds.
 * Returns -1 if timestamps not set.
 */
double timing_get_rtt_ms(const timing_info_t *t);

/*
 * Get body transfer time in milliseconds.
 * Returns -1 if timestamps not set.
 */
double timing_get_body_time_ms(const timing_info_t *t);

/*
 * Sleep for specified milliseconds.
 */
void timing_sleep_ms(int ms);

#endif /* NETSPEED_TIMING_H */
