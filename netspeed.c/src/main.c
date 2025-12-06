/*
 * main.c - netspeed CLI entry point
 *
 * Parse arguments and run speed test.
 */

#include "types.h"
#include "http.h"
#include "speedtest.h"
#include "output.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>

/* Global speedtest context for signal handling */
static speedtest_t *g_speedtest = NULL;

static void signal_handler(int sig)
{
    (void)sig;
    if (g_speedtest) {
        speedtest_abort(g_speedtest);
    }
}

static void print_usage(const char *prog)
{
    printf("Usage: %s [OPTIONS] [SERVER_URL]\n", prog);
    printf("\n");
    printf("Options:\n");
    printf("  -s, --server URL     Speed test server URL (required)\n");
    printf("  -j, --json           Output results as JSON\n");
    printf("  -c, --csv            Output results as CSV\n");
    printf("  -q, --quiet          Minimal output (just numbers)\n");
    printf("  -v, --verbose        Show detailed test information\n");
    printf("  -f, --quick          Run quick test with fewer probes\n");
    printf("  -d, --download-only  Run download test only\n");
    printf("  -u, --upload-only    Run upload test only\n");
    printf("  --no-packet-loss     Skip packet loss test\n");
    printf("  --no-color           Disable colored output\n");
    printf("  --timeout SECONDS    Request timeout (default: 120)\n");
    printf("  -h, --help           Show this help\n");
    printf("  -V, --version        Show version\n");
    printf("\n");
    printf("Examples:\n");
    printf("  %s -s https://speed.example.com\n", prog);
    printf("  %s -s https://speed.example.com --json\n", prog);
    printf("  %s -s https://speed.example.com -v --download-only\n", prog);
    printf("\n");
}

static void print_version(void)
{
    printf("netspeed %s\n", NETSPEED_VERSION);
}

static int parse_args(int argc, char *argv[], config_t *cfg)
{
    memset(cfg, 0, sizeof(*cfg));
    cfg->timeout_sec = REQUEST_TIMEOUT_SEC;

    for (int i = 1; i < argc; i++) {
        const char *arg = argv[i];

        if (strcmp(arg, "-h") == 0 || strcmp(arg, "--help") == 0) {
            print_usage(argv[0]);
            exit(0);
        } else if (strcmp(arg, "-V") == 0 || strcmp(arg, "--version") == 0) {
            print_version();
            exit(0);
        } else if (strcmp(arg, "-s") == 0 || strcmp(arg, "--server") == 0) {
            if (i + 1 >= argc) {
                fprintf(stderr, "Error: --server requires an argument\n");
                return -1;
            }
            strncpy(cfg->server_url, argv[++i], sizeof(cfg->server_url) - 1);
        } else if (strcmp(arg, "-j") == 0 || strcmp(arg, "--json") == 0) {
            cfg->json_output = true;
        } else if (strcmp(arg, "-c") == 0 || strcmp(arg, "--csv") == 0) {
            cfg->csv_output = true;
        } else if (strcmp(arg, "-q") == 0 || strcmp(arg, "--quiet") == 0) {
            cfg->quiet = true;
        } else if (strcmp(arg, "-v") == 0 || strcmp(arg, "--verbose") == 0) {
            cfg->verbose = true;
        } else if (strcmp(arg, "-f") == 0 || strcmp(arg, "--quick") == 0) {
            cfg->quick = true;
        } else if (strcmp(arg, "-d") == 0 || strcmp(arg, "--download-only") == 0) {
            cfg->download_only = true;
        } else if (strcmp(arg, "-u") == 0 || strcmp(arg, "--upload-only") == 0) {
            cfg->upload_only = true;
        } else if (strcmp(arg, "--no-packet-loss") == 0) {
            cfg->skip_packet_loss = true;
        } else if (strcmp(arg, "--no-color") == 0) {
            cfg->no_color = true;
        } else if (strcmp(arg, "--timeout") == 0) {
            if (i + 1 >= argc) {
                fprintf(stderr, "Error: --timeout requires an argument\n");
                return -1;
            }
            cfg->timeout_sec = atoi(argv[++i]);
        } else if (arg[0] == '-') {
            fprintf(stderr, "Error: Unknown option: %s\n", arg);
            return -1;
        } else {
            /* Positional argument: server URL */
            if (cfg->server_url[0] == '\0') {
                strncpy(cfg->server_url, arg, sizeof(cfg->server_url) - 1);
            }
        }
    }

    /* Validate required arguments */
    if (cfg->server_url[0] == '\0') {
        fprintf(stderr, "Error: Server URL is required. Use -s or --server.\n");
        print_usage(argv[0]);
        return -1;
    }

    /* Validate URL scheme */
    if (strncmp(cfg->server_url, "http://", 7) != 0 &&
        strncmp(cfg->server_url, "https://", 8) != 0) {
        fprintf(stderr, "Error: Server URL must start with http:// or https://\n");
        return -1;
    }

    /* Validate conflicting options */
    if (cfg->download_only && cfg->upload_only) {
        fprintf(stderr, "Error: Cannot use both --download-only and --upload-only\n");
        return -1;
    }

    int output_modes = 0;
    if (cfg->json_output) output_modes++;
    if (cfg->csv_output) output_modes++;
    if (cfg->quiet) output_modes++;
    if (output_modes > 1) {
        fprintf(stderr, "Error: Only one of --json, --csv, or --quiet can be specified\n");
        return -1;
    }

    return 0;
}

/* Progress callback */
static output_t *g_output = NULL;

static void progress_callback(const char *phase, int current, int total, double value)
{
    if (g_output) {
        output_progress(g_output, phase, current, total, value);
    }
}

int main(int argc, char *argv[])
{
    config_t config;
    speedtest_t st;
    output_t out;
    int ret = 0;

    /* Parse command line */
    if (parse_args(argc, argv, &config) < 0) {
        return 1;
    }

    /* Initialize SSL */
    ssl_init();

    /* Initialize output */
    output_init(&out, &config);
    g_output = &out;

    /* Initialize speedtest */
    speedtest_init(&st, &config);
    speedtest_set_progress(&st, progress_callback);
    g_speedtest = &st;

    /* Setup signal handlers */
    signal(SIGINT, signal_handler);
    signal(SIGTERM, signal_handler);

    /* Print header */
    output_header(&out, config.server_url);

    /* Run speed test */
    int err = speedtest_run(&st);

    if (err != ERR_OK && err != ERR_TIMEOUT) {
        const char *err_msg;
        switch (err) {
        case ERR_NETWORK:
            err_msg = "Network error - check connection and server URL";
            break;
        case ERR_DNS:
            err_msg = "DNS resolution failed";
            break;
        case ERR_TLS:
            err_msg = "TLS/SSL handshake failed";
            break;
        case ERR_HTTP:
            err_msg = "HTTP protocol error";
            break;
        case ERR_PARSE:
            err_msg = "Failed to parse server response";
            break;
        case ERR_MEMORY:
            err_msg = "Out of memory";
            break;
        case ERR_ARGS:
            err_msg = "Invalid arguments";
            break;
        default:
            err_msg = "Unknown error";
            break;
        }
        output_error(&out, err_msg);
        ret = 1;
    } else {
        /* Output results */
        const results_t *r = speedtest_results(&st);

        if (config.json_output) {
            output_json(r);
        } else if (config.csv_output) {
            output_csv(r);
        } else if (config.quiet) {
            output_quiet(r);
        } else {
            output_results(&out, r);
            output_verbose(&out, r);
        }
    }

    /* Cleanup */
    speedtest_cleanup(&st);
    ssl_cleanup();

    return ret;
}
