/*
 * output.h - Terminal output, spinners, and colors
 */

#ifndef NETSPEED_OUTPUT_H
#define NETSPEED_OUTPUT_H

#include "types.h"
#include <stdbool.h>

/* ANSI color codes */
#define COLOR_RESET   "\033[0m"
#define COLOR_RED     "\033[31m"
#define COLOR_GREEN   "\033[32m"
#define COLOR_YELLOW  "\033[33m"
#define COLOR_BLUE    "\033[34m"
#define COLOR_CYAN    "\033[36m"
#define COLOR_BOLD    "\033[1m"
#define COLOR_DIM     "\033[2m"

/* Output context */
typedef struct {
    bool use_color;
    bool interactive;
    bool quiet;
    bool verbose;
    bool json_mode;
    bool csv_mode;
} output_t;

/*
 * Initialize output context.
 */
void output_init(output_t *out, const config_t *cfg);

/*
 * Check if stdout is a terminal (interactive mode).
 */
bool output_is_tty(void);

/*
 * Print header with server info.
 */
void output_header(const output_t *out, const char *server_url);

/*
 * Update progress display.
 */
void output_progress(const output_t *out, const char *phase,
                     int current, int total, double value);

/*
 * Clear progress line.
 */
void output_clear_progress(const output_t *out);

/*
 * Print final results (normal format).
 */
void output_results(const output_t *out, const results_t *r);

/*
 * Print results as JSON.
 */
void output_json(const results_t *r);

/*
 * Print results as CSV.
 */
void output_csv(const results_t *r);

/*
 * Print quiet output (just numbers).
 */
void output_quiet(const results_t *r);

/*
 * Print error message.
 */
void output_error(const output_t *out, const char *msg);

/*
 * Print verbose details.
 */
void output_verbose(const output_t *out, const results_t *r);

/* ===================== Spinner ===================== */

/* Spinner state */
typedef struct {
    const char **frames;
    int frame_count;
    int current;
    char prefix[64];
    bool running;
} spinner_t;

/*
 * Initialize spinner.
 */
void spinner_init(spinner_t *s);

/*
 * Start spinner with prefix text.
 */
void spinner_start(spinner_t *s, const char *prefix);

/*
 * Update spinner (call in loop).
 */
void spinner_update(spinner_t *s);

/*
 * Stop spinner and print final text.
 */
void spinner_stop(spinner_t *s, const char *final_text);

/* ===================== Progress Bar ===================== */

/*
 * Print progress bar.
 * percent: 0.0 to 1.0
 * width: bar width in characters
 */
void progress_bar(double percent, int width, char *buf, size_t buf_len);

/* ===================== Color Helpers ===================== */

/*
 * Wrap text in color code if colors enabled.
 */
const char *color_wrap(const output_t *out, const char *color, const char *text);

/*
 * Get color for grade (Great=green, Good=green, Okay=yellow, Poor=red).
 */
const char *grade_color(const output_t *out, const char *grade);

#endif /* NETSPEED_OUTPUT_H */
