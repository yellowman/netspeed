/* main.c - First-class protocol-v2 native C CLI. */
#include "http.h"
#include "output.h"
#include "speedtest.h"
#include "types.h"

#include <ctype.h>
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static speedtest_t *global_test;
static output_t *global_output;

static void signal_handler(int signal_number)
{
    (void)signal_number;
    if (global_test) speedtest_abort(global_test);
}

static void usage(const char *program)
{
    fprintf(stderr, "Usage: %s [flags] [server-url]\n\n", program);
    fprintf(stderr, "A native C speed-test client for netspeedd measurement protocol v2.\n\n");
    fprintf(stderr, "Flags:\n");
    fprintf(stderr, "  -s, --server URL       Server URL (default: %s)\n", DEFAULT_SERVER_URL);
    fprintf(stderr, "      --token TOKEN      Shared bearer token (or NETSPEED_TOKEN)\n");
    fprintf(stderr, "  -j, --json             Output results as JSON\n");
    fprintf(stderr, "  -c, --csv              Output results as CSV\n");
    fprintf(stderr, "      --quiet            Minimal output (final values only)\n");
    fprintf(stderr, "  -v, --verbose          Show detailed progress and samples\n");
    fprintf(stderr, "  -q, --quick            Quick mode (fewer samples/windows)\n");
    fprintf(stderr, "  -d, --download-only    Skip upload tests\n");
    fprintf(stderr, "  -u, --upload-only      Skip download tests\n");
    fprintf(stderr, "      --no-packet-loss   Skip the exact-size WebRTC packet test\n");
    fprintf(stderr, "      --no-color         Disable terminal colors\n");
    fprintf(stderr, "  -t, --timeout DURATION Total timeout (default: 60s)\n");
    fprintf(stderr, "  -V, --version          Show version and exit\n");
    fprintf(stderr, "  -h, --help             Show this help\n\n");
    fprintf(stderr, "Examples:\n");
    fprintf(stderr, "  %s\n", program);
    fprintf(stderr, "  %s --quick https://speed.example.com\n", program);
    fprintf(stderr, "  %s --token secret --json\n", program);
}

static void print_version(void)
{
    printf("netspeed %s commit=%s date=%s\n", NETSPEED_VERSION, NETSPEED_COMMIT, NETSPEED_BUILD_DATE);
}

static int64_t parse_duration_ms(const char *text)
{
    if (!text || !*text) return -1;
    errno = 0;
    char *end = NULL;
    double value = strtod(text, &end);
    if (errno != 0 || end == text || value <= 0) return -1;
    double multiplier = 1000.0;
    if (*end == '\0' || strcmp(end, "s") == 0) {
        multiplier = 1000.0;
    } else if (strcmp(end, "ms") == 0) {
        multiplier = 1.0;
    } else if (strcmp(end, "m") == 0) {
        multiplier = 60000.0;
    } else if (strcmp(end, "h") == 0) {
        multiplier = 3600000.0;
    } else {
        return -1;
    }
    double milliseconds = value * multiplier;
    if (milliseconds > 86400000.0) return -1;
    return (int64_t)milliseconds;
}

static int copy_argument(char *destination, size_t capacity, const char *value,
                         const char *name)
{
    if (!value || strlen(value) >= capacity) {
        fprintf(stderr, "Error: %s is too long\n", name);
        return -1;
    }
    snprintf(destination, capacity, "%s", value);
    return 0;
}

static int parse_args(int argc, char **argv, config_t *config)
{
    memset(config, 0, sizeof(*config));
    snprintf(config->server_url, sizeof(config->server_url), "%s", DEFAULT_SERVER_URL);
    config->timeout_ms = DEFAULT_TEST_TIMEOUT_MS;
    bool server_set = false;

    for (int index = 1; index < argc; index++) {
        const char *argument = argv[index];
        if (strcmp(argument, "-h") == 0 || strcmp(argument, "--help") == 0) {
            usage(argv[0]);
            exit(0);
        } else if (strcmp(argument, "-V") == 0 || strcmp(argument, "--version") == 0) {
            print_version();
            exit(0);
        } else if (strcmp(argument, "-s") == 0 || strcmp(argument, "--server") == 0) {
            if (++index >= argc || copy_argument(config->server_url, sizeof(config->server_url),
                                                 argv[index], "server URL") != 0) return -1;
            server_set = true;
        } else if (strcmp(argument, "--token") == 0) {
            if (++index >= argc || copy_argument(config->access_token, sizeof(config->access_token),
                                                 argv[index], "access token") != 0) return -1;
        } else if (strcmp(argument, "-j") == 0 || strcmp(argument, "--json") == 0) {
            config->json_output = true;
        } else if (strcmp(argument, "-c") == 0 || strcmp(argument, "--csv") == 0) {
            config->csv_output = true;
        } else if (strcmp(argument, "--quiet") == 0) {
            config->quiet = true;
        } else if (strcmp(argument, "-v") == 0 || strcmp(argument, "--verbose") == 0) {
            config->verbose = true;
        } else if (strcmp(argument, "-q") == 0 || strcmp(argument, "--quick") == 0) {
            config->quick = true;
        } else if (strcmp(argument, "-d") == 0 || strcmp(argument, "--download-only") == 0) {
            config->download_only = true;
        } else if (strcmp(argument, "-u") == 0 || strcmp(argument, "--upload-only") == 0) {
            config->upload_only = true;
        } else if (strcmp(argument, "--no-packet-loss") == 0) {
            config->skip_packet_loss = true;
        } else if (strcmp(argument, "--no-color") == 0) {
            config->no_color = true;
        } else if (strcmp(argument, "-t") == 0 || strcmp(argument, "--timeout") == 0) {
            if (++index >= argc) {
                fprintf(stderr, "Error: %s requires a duration\n", argument);
                return -1;
            }
            config->timeout_ms = parse_duration_ms(argv[index]);
            if (config->timeout_ms <= 0) {
                fprintf(stderr, "Error: invalid timeout duration %s\n", argv[index]);
                return -1;
            }
        } else if (argument[0] == '-') {
            fprintf(stderr, "Error: unknown option %s\n", argument);
            return -1;
        } else if (!server_set) {
            if (copy_argument(config->server_url, sizeof(config->server_url), argument,
                              "server URL") != 0) return -1;
            server_set = true;
        } else {
            fprintf(stderr, "Error: unexpected positional argument %s\n", argument);
            return -1;
        }
    }

    if (config->access_token[0] == '\0') {
        const char *environment_token = getenv("NETSPEED_TOKEN");
        if (environment_token && copy_argument(config->access_token, sizeof(config->access_token),
                                               environment_token, "NETSPEED_TOKEN") != 0) return -1;
    }
    if (strncmp(config->server_url, "http://", 7) != 0 &&
        strncmp(config->server_url, "https://", 8) != 0) {
        fprintf(stderr, "Error: server URL must start with http:// or https://\n");
        return -1;
    }
    size_t server_length = strlen(config->server_url);
    while (server_length > 8 && config->server_url[server_length - 1] == '/') {
        config->server_url[--server_length] = '\0';
    }
    if (config->download_only && config->upload_only) {
        fprintf(stderr, "Error: --download-only and --upload-only are mutually exclusive\n");
        return -1;
    }
    int output_modes = config->json_output + config->csv_output + config->quiet;
    if (output_modes > 1) {
        fprintf(stderr, "Error: choose only one of --json, --csv, or --quiet\n");
        return -1;
    }
    return 0;
}

static void progress_callback(const char *stage, int current, int total, double value)
{
    if (global_output) output_progress(global_output, stage, current, total, value);
}

int main(int argc, char **argv)
{
    config_t config;
    if (parse_args(argc, argv, &config) != 0) {
        usage(argv[0]);
        return 2;
    }
    if (http_global_init() != ERR_OK) {
        fprintf(stderr, "Error: failed to initialize libcurl\n");
        return 1;
    }

    output_t output;
    output_init(&output, &config);
    global_output = &output;
    speedtest_t test;
    speedtest_init(&test, &config);
    speedtest_set_progress(&test, progress_callback);
    global_test = &test;

    signal(SIGINT, signal_handler);
    signal(SIGTERM, signal_handler);
#ifdef SIGPIPE
    signal(SIGPIPE, SIG_IGN);
#endif

    output_header(&output, config.server_url);
    int status = speedtest_run(&test);
    if (status != ERR_OK) {
        output_error(&output, speedtest_error(&test));
        speedtest_cleanup(&test);
        http_global_cleanup();
        return 1;
    }
    const results_t *results = speedtest_results(&test);
    if (config.json_output) {
        output_json(results);
    } else if (config.csv_output) {
        output_csv(results);
    } else if (config.quiet) {
        output_quiet(results);
    } else {
        output_results(&output, results);
        output_verbose(&output, results);
    }

    speedtest_cleanup(&test);
    http_global_cleanup();
    return 0;
}
