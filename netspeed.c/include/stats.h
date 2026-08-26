/* stats.h - Shared R-7/IQR statistics and grading rules. */
#ifndef NETSPEED_STATS_H
#define NETSPEED_STATS_H

#include "types.h"

#include <stddef.h>

void stats_sort(double *values, size_t count);
size_t stats_clean(const double *values, size_t count, double *out, size_t out_capacity);
double stats_percentile(const double *values, size_t count, double percentile);
size_t stats_filter_iqr(const double *values, size_t count, double *out, size_t out_capacity);
size_t stats_prepare_latency(const double *values, size_t count, size_t warmup,
                             double *out, size_t out_capacity);
double stats_jitter(const double *values, size_t count);
double stats_coefficient_of_variation(const double *values, size_t count);
double bytes_to_mbps(int64_t bytes, double duration_ms);

void calculate_summary(results_t *results);
void calculate_quality(results_t *results);
void assess_test_confidence(results_t *results, const config_t *config);

#endif
