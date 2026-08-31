#include "json.h"
#include "measurement_transport.h"
#include "packet_loss.h"
#include "stats.h"
#include "types.h"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s (%s:%d)\n", message, __FILE__, __LINE__); \
        failures++; \
    } \
} while (0)

static int close_enough(double actual, double expected, double tolerance)
{
    return fabs(actual - expected) <= tolerance;
}

static void test_packet_frame(void)
{
    uint8_t frame[NETSPEED_PACKET_FRAME_SIZE];
    packet_frame_encode_probe(frame, 42U, 1234567890LL);
    bool acknowledgement = true;
    uint32_t sequence = 0;
    int64_t sent_at = 0;
    int64_t received_at = -1;
    CHECK(packet_frame_decode(frame, sizeof(frame), &acknowledgement, &sequence,
                              &sent_at, &received_at) == 0,
          "exact 1,200-byte probe decodes");
    CHECK(!acknowledgement, "probe is not misclassified as an acknowledgement");
    CHECK(sequence == 42U, "packet sequence survives encoding");
    CHECK(sent_at == 1234567890LL, "packet timestamp survives encoding");
    CHECK(received_at == 0, "probe receiver timestamp is zero");
    CHECK(packet_frame_decode(frame, sizeof(frame) - 1, NULL, NULL, NULL, NULL) != 0,
          "short frame is rejected");
    frame[NETSPEED_PACKET_FRAME_SIZE - 1] ^= 0x01U;
    CHECK(packet_frame_decode(frame, sizeof(frame), NULL, NULL, NULL, NULL) != 0,
          "corrupt deterministic padding is rejected");
}

static void test_statistics(void)
{
    const double values[] = {1, 2, 3, 4};
    CHECK(close_enough(stats_percentile(values, 4, 90), 3.7, 0.000001),
          "R-7 p90 interpolation matches the Go/browser definition");
    CHECK(close_enough(stats_jitter(values, 4), 1.2, 0.000001),
          "jitter is p90 minus median");

    const double outlier_values[] = {1, 2, 2, 3, 100};
    double filtered[8] = {0};
    size_t filtered_count = stats_filter_iqr(outlier_values, 5, filtered, 8);
    CHECK(filtered_count == 4, "1.5-IQR filtering removes an isolated outlier");
    CHECK(close_enough(filtered[3], 3, 0.000001), "filtered values stay sorted");

    const double latency[] = {500, 400, 10, 11, 12};
    double prepared[8] = {0};
    size_t prepared_count = stats_prepare_latency(latency, 5, 2, prepared, 8);
    CHECK(prepared_count == 3, "latency preparation removes two warmup samples");
    CHECK(close_enough(stats_percentile(prepared, prepared_count, 50), 11, 0.000001),
          "prepared latency median is correct");

    CHECK(close_enough(bytes_to_mbps(1000000, 100), 80.0, 0.000001),
          "byte duration conversion uses decimal Mbps");
}

static void add_window(results_t *results, const char *direction, double mbps, int index)
{
    throughput_sample_t *sample = &results->throughput_samples[results->throughput_count++];
    memset(sample, 0, sizeof(*sample));
    snprintf(sample->direction, sizeof(sample->direction), "%s", direction);
    snprintf(sample->profile, sizeof(sample->profile), "%s", "window");
    snprintf(sample->sample_kind, sizeof(sample->sample_kind), "%s", "window");
    snprintf(sample->timing_source, sizeof(sample->timing_source), "%s", "aggregate-wall-clock");
    sample->duration_ms = 1500;
    sample->size_bytes = (int64_t)(mbps * 1500.0 * 1000.0 / 8.0);
    sample->mbps = mbps;
    sample->window_index = index;
    sample->has_window_index = true;
    sample->concurrency = 2;
    sample->chunk_bytes = 1000000;
    sample->request_count = 4;
}

static void add_latency(results_t *results, const char *condition, double rtt,
                        int index, bool loaded)
{
    latency_sample_t *sample = &results->latency_samples[results->latency_count++];
    memset(sample, 0, sizeof(*sample));
    sample->ts = 1700000000000LL + index;
    sample->started_at = sample->ts - 1;
    sample->ended_at = sample->ts;
    sample->rtt_ms = rtt;
    snprintf(sample->condition, sizeof(sample->condition), "%s", condition);
    snprintf(sample->timing_source, sizeof(sample->timing_source), "%s", "libcurl-first-byte");
    sample->connection_reused = true;
    snprintf(sample->probe_transport, sizeof(sample->probe_transport), "%s", "http");
    snprintf(sample->probe_method, sizeof(sample->probe_method), "%s", "GET");
    snprintf(sample->probe_path, sizeof(sample->probe_path), "%s", "/measure/ping");
    sample->load_overlapped = loaded;
    sample->load_tracking_accurate = loaded;
}

static void test_summary_and_json(void)
{
    results_t results;
    memset(&results, 0, sizeof(results));
    snprintf(results.meta.hostname, sizeof(results.meta.hostname), "%s", "mock");
    snprintf(results.meta.client_ip, sizeof(results.meta.client_ip), "%s", "127.0.0.1");
    snprintf(results.meta.http_protocol, sizeof(results.meta.http_protocol), "%s", "HTTP/1.1");
    results.meta.max_transfer_bytes = 8388608;
    results.meta.max_concurrent_transfers_per_client = 8;
    results.meta.measurement_protocol_version = 2;
    results.meta.upload_receipt_version = 1;
    results.meta.packet_loss_frame_version = 1;
    measurement_selection_legacy(&results.meta.measurement_selection);
    results.meta.measurement_selection_present = true;
    snprintf(results.start_time_rfc3339, sizeof(results.start_time_rfc3339),
             "%s", "2026-08-25T12:00:00.000Z");
    snprintf(results.end_time_rfc3339, sizeof(results.end_time_rfc3339),
             "%s", "2026-08-25T12:00:05.000Z");

    add_window(&results, "download", 100, 0);
    add_window(&results, "download", 110, 1);
    add_window(&results, "download", 120, 2);
    add_window(&results, "upload", 50, 0);
    add_window(&results, "upload", 60, 1);
    add_window(&results, "upload", 70, 2);
    for (int index = 0; index < 12; index++) {
        add_latency(&results, "unloaded", 10 + index, index, false);
    }
    for (int index = 0; index < 5; index++) {
        add_latency(&results, "download", 20 + index, 100 + index, true);
        add_latency(&results, "upload", 25 + index, 200 + index, true);
    }

    calculate_summary(&results);
    calculate_quality(&results);
    config_t config;
    memset(&config, 0, sizeof(config));
    assess_test_confidence(&results, &config);

    CHECK(close_enough(results.summary.download_mbps, 118, 0.000001),
          "headline download is fixed-window R-7 p90");
    CHECK(close_enough(results.summary.upload_mbps, 68, 0.000001),
          "headline upload is fixed-window R-7 p90");
    CHECK(!results.summary.packet_loss_available,
          "unmeasured packet loss remains unavailable");
    CHECK(strcmp(results.quality.gaming, "Incomplete") == 0,
          "unknown loss cannot improve the quality grade");
    CHECK(!results.test_confidence.packet_test_completed,
          "confidence records the missing packet test");

    char *encoded = results_to_json(&results);
    CHECK(encoded != NULL, "results serialize to JSON");
    json_value_t *root = json_parse(encoded ? encoded : "");
    CHECK(root != NULL, "serialized results parse as JSON");
    if (root) {
        json_value_t *summary = json_get(root, "summary");
        json_value_t *packet_summary = summary ? json_get(summary, "packetLossPercent") : NULL;
        CHECK(packet_summary && packet_summary->type == JSON_NULL,
              "summary packetLossPercent is JSON null when unavailable");
        json_value_t *packet = json_get(root, "packetLoss");
        CHECK(packet && packet->type == JSON_NULL,
              "skipped packet test serializes as JSON null");
        CHECK(strcmp(json_get_string(root, "startTime"),
                     "2026-08-25T12:00:00.000Z") == 0,
              "result time is RFC3339 text like the Go client");
        json_value_t *throughput = json_get(root, "throughputSamples");
        CHECK(throughput && throughput->type == JSON_ARRAY,
              "throughputSamples is present");
        json_value_t *meta = json_get(root, "meta");
        json_value_t *selection = meta ? json_get(meta, "measurementSelection") : NULL;
        CHECK(selection && selection->type == JSON_OBJECT,
              "measurementSelection is serialized");
        json_value_t *latency = json_get(root, "latencySamples");
        json_value_t *first_latency = latency && latency->type == JSON_ARRAY &&
                                      latency->u.array ? latency->u.array->value : NULL;
        CHECK(first_latency && json_get_bool(first_latency, "connectionReused", false),
              "latency connection reuse evidence is serialized");
        CHECK(first_latency && strcmp(json_get_string(first_latency, "probePath"),
                                      "/measure/ping") == 0,
              "latency endpoint evidence is serialized");
    }
    json_free(root);
    free(encoded);
}

static const char *capability_json(void)
{
    return "{\"hostname\":\"mock\",\"clientIp\":\"127.0.0.1\","
           "\"httpProtocol\":\"HTTP/1.1\",\"maxTransferBytes\":8388608,"
           "\"maxConcurrentTransfersPerClient\":8,"
           "\"measurementProtocolVersion\":2,\"uploadReceiptVersion\":1,"
           "\"packetLossFrameVersion\":1,\"measurementCapabilities\":{"
           "\"version\":1,\"downloadPath\":\"/measure/down\","
           "\"downloadBytesParameter\":\"n\","
           "\"downloadPayloadParameter\":\"kind\","
           "\"downloadFramingParameter\":\"wire\","
           "\"downloadChunkBytesParameter\":\"block\","
           "\"downloadFlushParameter\":\"emit\","
           "\"uploadPath\":\"/measure/up\","
           "\"uploadBytesParameter\":\"expected\","
           "\"httpPingPath\":\"/measure/ping\","
           "\"httpPingMethods\":[\"GET\",\"HEAD\"],"
           "\"warmConnectionPing\":true,"
           "\"downloadPayloads\":[\"random\",\"zero\"],"
           "\"downloadFramings\":[\"fixed\",\"chunked\"],"
           "\"defaultDownloadPayload\":\"random\","
           "\"defaultDownloadFraming\":\"fixed\","
           "\"defaultChunkBytes\":65536,\"minimumChunkBytes\":4096,"
           "\"maximumChunkBytes\":1048576,"
           "\"uploadContentEncodings\":[\"identity\"],"
           "\"responseCacheControl\":\"no-store, no-transform\","
           "\"noTransform\":true,"
           "\"proxyBufferSuppressionHeader\":\"X-Accel-Buffering: no\","
           "\"proxyRequestBufferingAdvisory\":true}}";
}

static void test_meta_parser(void)
{
    meta_t meta;
    CHECK(meta_from_json(capability_json(), &meta) == 0,
          "protocol-v2 metadata and transport capabilities parse");
    CHECK(meta.measurement_protocol_version == 2, "protocol version is retained");
    CHECK(meta.max_transfer_bytes == 8388608, "64-bit transfer limit is retained");
    CHECK(meta.max_concurrent_transfers_per_client == 8,
          "per-client concurrency capability is retained");
    CHECK(meta.measurement_capabilities.present &&
          strcmp(meta.measurement_capabilities.download_path, "/measure/down") == 0,
          "custom measurement path is retained");
    CHECK(meta.measurement_capabilities.http_ping_get &&
          meta.measurement_capabilities.http_ping_head,
          "advertised latency methods are retained");
    CHECK(meta.measurement_capabilities.download_payload_random &&
          meta.measurement_capabilities.download_payload_zero,
          "advertised payload discriminators are retained");
}

static void test_transport_negotiation(void)
{
    meta_t meta;
    CHECK(meta_from_json(capability_json(), &meta) == 0,
          "capability fixture parses for negotiation");
    config_t config;
    memset(&config, 0, sizeof(config));
    snprintf(config.download_payload, sizeof(config.download_payload), "%s", "zero");
    snprintf(config.download_framing, sizeof(config.download_framing), "%s", "chunked");
    snprintf(config.download_flush, sizeof(config.download_flush), "%s", "false");
    config.download_chunk_bytes = 8192;
    measurement_selection_t selection;
    char error[MAX_ERROR_LEN] = {0};
    CHECK(measurement_negotiate(&meta.measurement_capabilities, &config, &selection,
                                error, sizeof(error)) == ERR_OK,
          "explicit C transport preferences negotiate");
    CHECK(strcmp(selection.download_payload, "zero") == 0 &&
          strcmp(selection.download_framing, "chunked") == 0 &&
          selection.download_chunk_bytes == 8192 && !selection.download_flush,
          "negotiated discriminator values are exact");
    CHECK(strcmp(selection.latency_path, "/measure/ping") == 0 &&
          strcmp(selection.latency_method, "GET") == 0 &&
          selection.warm_connection_ping,
          "dedicated warm latency endpoint is selected");

    char path[MAX_URL_LEN];
    CHECK(measurement_build_download_path(NULL, &selection, 12345, "unit", 7,
                                          "download", path, sizeof(path)) == ERR_OK,
          "download measurement path builds");
    CHECK(strstr(path, "/measure/down?") == path && strstr(path, "n=12345") &&
          strstr(path, "kind=zero") && strstr(path, "wire=chunked") &&
          strstr(path, "block=8192") && strstr(path, "emit=false"),
          "download path uses advertised keys and selected values");
    CHECK(measurement_build_upload_path(NULL, &selection, 777, "unit", 3,
                                        path, sizeof(path)) == ERR_OK &&
          strstr(path, "/measure/up?") == path && strstr(path, "expected=777"),
          "upload path uses the advertised expected-byte key");
    CHECK(measurement_build_latency_path(NULL, &selection, "unloaded", 2, 1,
                                         path, sizeof(path)) == ERR_OK &&
          strstr(path, "/measure/ping?") == path && strstr(path, "during=unloaded") &&
          strstr(path, "attempt=1"),
          "latency path uses the dedicated advertised endpoint");

    http_response_t response;
    memset(&response, 0, sizeof(response));
    response.content_length = -1;
    response.chunk_bytes = 8192;
    response.transferred_bytes = 12345;
    snprintf(response.cache_control, sizeof(response.cache_control), "%s",
             "no-store, no-transform");
    snprintf(response.x_accel_buffering, sizeof(response.x_accel_buffering), "%s", "no");
    snprintf(response.measurement, sizeof(response.measurement), "%s", "download");
    snprintf(response.payload, sizeof(response.payload), "%s", "zero");
    snprintf(response.framing, sizeof(response.framing), "%s", "chunked");
    snprintf(response.flush, sizeof(response.flush), "%s", "false");
    snprintf(response.transfer_encoding, sizeof(response.transfer_encoding), "%s", "chunked");
    snprintf(response.http_protocol, sizeof(response.http_protocol), "%s", "HTTP/1.1");
    CHECK(measurement_verify_download(&selection, &response, 12345, "download",
                                      error, sizeof(error)) == ERR_OK,
          "chunked response contract verifies");
    snprintf(response.content_encoding, sizeof(response.content_encoding), "%s",
             "identity, identity");
    CHECK(measurement_verify_download(&selection, &response, 12345, "download",
                                      error, sizeof(error)) == ERR_OK,
          "repeated identity content codings remain valid");
    snprintf(response.content_encoding, sizeof(response.content_encoding), "%s",
             "identity, gzip");
    CHECK(measurement_verify_download(&selection, &response, 12345, "download",
                                      error, sizeof(error)) == ERR_PROTOCOL,
          "a compressed coding cannot hide behind identity");
    snprintf(response.content_encoding, sizeof(response.content_encoding), "%s",
             "identity");
    snprintf(response.flush, sizeof(response.flush), "%s", "true");
    CHECK(measurement_verify_download(&selection, &response, 12345, "download",
                                      error, sizeof(error)) == ERR_PROTOCOL,
          "download flush evidence must match the negotiated selection");
    snprintf(response.flush, sizeof(response.flush), "%s", "false");

    config_t head_config;
    measurement_capabilities_t head_only = meta.measurement_capabilities;
    head_only.http_ping_get = false;
    head_only.http_ping_head = true;
    memset(&head_config, 0, sizeof(head_config));
    snprintf(head_config.download_payload, sizeof(head_config.download_payload),
             "%s", "auto");
    snprintf(head_config.download_framing, sizeof(head_config.download_framing),
             "%s", "auto");
    snprintf(head_config.download_flush, sizeof(head_config.download_flush),
             "%s", "auto");
    CHECK(measurement_negotiate(&head_only, &head_config, &selection,
                                error, sizeof(error)) == ERR_OK &&
          strcmp(selection.latency_method, "HEAD") == 0,
          "HEAD is selected when it is the only advertised HTTP ping method");

    config_t legacy_config;
    memset(&legacy_config, 0, sizeof(legacy_config));
    snprintf(legacy_config.download_payload, sizeof(legacy_config.download_payload),
             "%s", "auto");
    snprintf(legacy_config.download_framing, sizeof(legacy_config.download_framing),
             "%s", "auto");
    snprintf(legacy_config.download_flush, sizeof(legacy_config.download_flush),
             "%s", "auto");
    measurement_capabilities_t missing;
    memset(&missing, 0, sizeof(missing));
    CHECK(measurement_negotiate(&missing, &legacy_config, &selection,
                                error, sizeof(error)) == ERR_OK &&
          selection.legacy_fallback,
          "automatic controls retain legacy protocol-v2 fallback");
    snprintf(legacy_config.download_payload, sizeof(legacy_config.download_payload),
             "%s", "zero");
    CHECK(measurement_negotiate(&missing, &legacy_config, &selection,
                                error, sizeof(error)) == ERR_ARGS,
          "explicit controls are not sent to a legacy endpoint");

    measurement_capabilities_t duplicate = meta.measurement_capabilities;
    snprintf(duplicate.download_flush_parameter,
             sizeof(duplicate.download_flush_parameter), "%s",
             duplicate.download_payload_parameter);
    CHECK(measurement_negotiate(&duplicate, &legacy_config, &selection,
                                error, sizeof(error)) == ERR_PROTOCOL,
          "conflicting advertised discriminator keys are rejected");

    measurement_capabilities_t unsafe = meta.measurement_capabilities;
    snprintf(unsafe.download_path, sizeof(unsafe.download_path), "%s", "//evil.test/down");
    memset(&legacy_config, 0, sizeof(legacy_config));
    snprintf(legacy_config.download_payload, sizeof(legacy_config.download_payload),
             "%s", "auto");
    snprintf(legacy_config.download_framing, sizeof(legacy_config.download_framing),
             "%s", "auto");
    snprintf(legacy_config.download_flush, sizeof(legacy_config.download_flush),
             "%s", "auto");
    CHECK(measurement_negotiate(&unsafe, &legacy_config, &selection,
                                error, sizeof(error)) == ERR_PROTOCOL,
          "cross-authority capability paths are rejected");
}

int main(void)
{
    test_packet_frame();
    test_statistics();
    test_summary_and_json();
    test_meta_parser();
    test_transport_negotiation();
    if (failures) {
        fprintf(stderr, "%d protocol test(s) failed\n", failures);
        return 1;
    }
    puts("C protocol-v2 unit tests passed");
    return 0;
}
