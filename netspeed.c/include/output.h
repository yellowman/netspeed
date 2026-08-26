/* output.h - Terminal and machine-readable C client output. */
#ifndef NETSPEED_OUTPUT_H
#define NETSPEED_OUTPUT_H

#include "types.h"

#include <stdbool.h>

typedef struct {
    bool use_color;
    bool interactive;
    bool quiet;
    bool verbose;
    bool json_mode;
    bool csv_mode;
} output_t;

void output_init(output_t *output, const config_t *config);
bool output_is_tty(void);
void output_header(const output_t *output, const char *server_url);
void output_progress(const output_t *output, const char *stage,
                     int current, int total, double value);
void output_clear_progress(const output_t *output);
void output_results(const output_t *output, const results_t *results);
void output_json(const results_t *results);
void output_csv(const results_t *results);
void output_quiet(const results_t *results);
void output_error(const output_t *output, const char *message);
void output_verbose(const output_t *output, const results_t *results);

#endif
