/* timing.h - Monotonic and wall-clock helpers. */
#ifndef NETSPEED_TIMING_H
#define NETSPEED_TIMING_H

#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>
#include <time.h>

void timing_now(struct timespec *ts);
int64_t timing_now_ms(void);
double timing_diff_ms(const struct timespec *start, const struct timespec *end);
double timing_diff_sec(const struct timespec *start, const struct timespec *end);
int64_t timing_monotonic_ms(void);
void timing_sleep_ms(int ms);
void timing_format_rfc3339_ms(int64_t unix_ms, char *buffer, size_t buffer_len);
bool timing_before_deadline(const struct timespec *deadline);
int64_t timing_remaining_ms(const struct timespec *deadline);

#endif
