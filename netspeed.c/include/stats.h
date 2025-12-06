/*
 * stats.h - Statistical functions
 */

#ifndef NETSPEED_STATS_H
#define NETSPEED_STATS_H

#include "types.h"

/*
 * Sort array of doubles in-place.
 */
void stats_sort(double *arr, int n);

/*
 * Calculate percentile of sorted array.
 * Array must be sorted.
 */
double stats_percentile(const double *sorted, int n, double p);

/*
 * Calculate median of sorted array.
 */
double stats_median(const double *sorted, int n);

/*
 * Calculate minimum of array.
 */
double stats_min(const double *arr, int n);

/*
 * Calculate maximum of array.
 */
double stats_max(const double *arr, int n);

/*
 * Calculate mean of array.
 */
double stats_mean(const double *arr, int n);

/*
 * Calculate jitter (p90 - p50).
 * Array must be sorted.
 */
double stats_jitter(const double *sorted, int n);

/*
 * Calculate interquartile range (p75 - p25).
 * Sorts array in-place.
 */
double stats_iqr(double *arr, int n);

/*
 * Calculate p90 (90th percentile).
 * Sorts array in-place.
 */
double stats_p90(double *arr, int n);

/*
 * Convert bytes and duration to Mbps.
 */
double bytes_to_mbps(int64_t bytes, double duration_ms);

/*
 * Calculate summary statistics from results.
 */
void calculate_summary(results_t *r);

/*
 * Calculate network quality grades.
 */
void calculate_quality(results_t *r);

/*
 * Grade video streaming quality.
 */
const char *grade_streaming(const summary_t *s);

/*
 * Grade gaming quality.
 */
const char *grade_gaming(const summary_t *s);

/*
 * Grade video chatting quality.
 */
const char *grade_video_chat(const summary_t *s);

#endif /* NETSPEED_STATS_H */
