/*
 * packet_loss.c - Exact-size WebRTC transaction/forward/reverse loss test.
 */
#include "packet_loss.h"

#include "json.h"
#include "stats.h"
#include "timing.h"

#include <inttypes.h>
#include <math.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#ifdef NETSPEED_HAVE_LIBDATACHANNEL
#include <rtc/rtc.h>
#endif

#define PACKET_HEADER_SIZE 32
#define PACKET_TYPE_PROBE 1
#define PACKET_TYPE_ACK 2
#define CONTROL_BODY_LIMIT (1024U * 1024U)

static void write_u16(uint8_t *destination, uint16_t value)
{
    destination[0] = (uint8_t)(value >> 8);
    destination[1] = (uint8_t)value;
}

static void write_u32(uint8_t *destination, uint32_t value)
{
    destination[0] = (uint8_t)(value >> 24);
    destination[1] = (uint8_t)(value >> 16);
    destination[2] = (uint8_t)(value >> 8);
    destination[3] = (uint8_t)value;
}

static void write_u64(uint8_t *destination, uint64_t value)
{
    for (int index = 7; index >= 0; index--) {
        destination[index] = (uint8_t)value;
        value >>= 8;
    }
}

static uint16_t read_u16(const uint8_t *source)
{
    return (uint16_t)((uint16_t)source[0] << 8 | source[1]);
}

static uint32_t read_u32(const uint8_t *source)
{
    return (uint32_t)source[0] << 24 | (uint32_t)source[1] << 16 |
           (uint32_t)source[2] << 8 | source[3];
}

static uint64_t read_u64(const uint8_t *source)
{
    uint64_t value = 0;
    for (int index = 0; index < 8; index++) {
        value = (value << 8) | source[index];
    }
    return value;
}

static void encode_frame(uint8_t frame[NETSPEED_PACKET_FRAME_SIZE], uint8_t type,
                         uint32_t sequence, int64_t sent_at_ms, int64_t received_at_ms)
{
    memset(frame, 0, NETSPEED_PACKET_FRAME_SIZE);
    memcpy(frame, "NSPL", 4);
    frame[4] = NETSPEED_PACKET_FRAME_VERSION;
    frame[5] = type;
    write_u16(frame + 6, PACKET_HEADER_SIZE);
    write_u32(frame + 8, sequence);
    write_u64(frame + 12, (uint64_t)sent_at_ms);
    write_u64(frame + 20, (uint64_t)received_at_ms);
    write_u32(frame + 28, NETSPEED_PACKET_FRAME_SIZE);
    for (size_t index = PACKET_HEADER_SIZE; index < NETSPEED_PACKET_FRAME_SIZE; index++) {
        frame[index] = (uint8_t)(((uint64_t)sequence + (uint64_t)index * 31U) & 0xffU);
    }
}

void packet_frame_encode_probe(uint8_t frame[NETSPEED_PACKET_FRAME_SIZE],
                               uint32_t sequence, int64_t sent_at_ms)
{
    encode_frame(frame, PACKET_TYPE_PROBE, sequence, sent_at_ms, 0);
}

int packet_frame_decode(const uint8_t frame[NETSPEED_PACKET_FRAME_SIZE], size_t size,
                        bool *acknowledgement, uint32_t *sequence,
                        int64_t *sent_at_ms, int64_t *received_at_ms)
{
    if (!frame || size != NETSPEED_PACKET_FRAME_SIZE || memcmp(frame, "NSPL", 4) != 0 ||
        frame[4] != NETSPEED_PACKET_FRAME_VERSION ||
        read_u16(frame + 6) != PACKET_HEADER_SIZE ||
        read_u32(frame + 28) != NETSPEED_PACKET_FRAME_SIZE) {
        return -1;
    }
    if (frame[5] != PACKET_TYPE_PROBE && frame[5] != PACKET_TYPE_ACK) {
        return -1;
    }
    uint32_t decoded_sequence = read_u32(frame + 8);
    for (size_t index = PACKET_HEADER_SIZE; index < NETSPEED_PACKET_FRAME_SIZE; index++) {
        uint8_t expected = (uint8_t)(((uint64_t)decoded_sequence + (uint64_t)index * 31U) & 0xffU);
        if (frame[index] != expected) {
            return -1;
        }
    }
    if (acknowledgement) *acknowledgement = frame[5] == PACKET_TYPE_ACK;
    if (sequence) *sequence = decoded_sequence;
    if (sent_at_ms) *sent_at_ms = (int64_t)read_u64(frame + 12);
    if (received_at_ms) *received_at_ms = (int64_t)read_u64(frame + 20);
    return 0;
}

#ifdef NETSPEED_HAVE_LIBDATACHANNEL
static double loss_percent(int sent, int received)
{
    if (sent <= 0) return 0;
    if (received < 0) received = 0;
    if (received > sent) received = sent;
    return (double)(sent - received) / (double)sent * 100.0;
}
#endif

static void unavailable(packet_loss_result_t *result, const char *reason)
{
    memset(result, 0, sizeof(*result));
    result->unavailable = true;
    result->frame_size_bytes = NETSPEED_PACKET_FRAME_SIZE;
    snprintf(result->reason, sizeof(result->reason), "%s", reason);
}

#ifndef NETSPEED_HAVE_LIBDATACHANNEL
int packet_loss_run(const packet_loss_config_t *config, packet_loss_result_t *result,
                    char *error, size_t error_len)
{
    (void)config;
    (void)error;
    (void)error_len;
    unavailable(result,
                "this C build lacks libdatachannel; rebuild with WEBRTC=yes for the protocol-v2 packet test");
    return ERR_OK;
}
#else

typedef struct {
    char username[256];
    char credential[256];
    int ttl_sec;
    char servers[MAX_ICE_SERVERS][MAX_ICE_SERVER_LEN];
    int server_count;
} turn_credentials_t;

typedef struct {
    bool ok;
    int protocol_version;
    int frame_size_bytes;
    int forward_received;
    int acknowledgements_sent;
    int duplicate_frames;
    int invalid_frames;
    int ack_send_failures;
} packet_report_t;

typedef struct {
    pthread_mutex_t mutex;
    int pc;
    int dc;
    bool gathering_complete;
    bool open;
    bool failed;
    bool closed;
    bool ack_seen[PACKET_LOSS_COUNT];
    bool sent_valid[PACKET_LOSS_COUNT];
    struct timespec sent_monotonic[PACKET_LOSS_COUNT];
    double rtts[PACKET_LOSS_COUNT];
    int ack_count;
} rtc_state_t;

static bool config_canceled(const packet_loss_config_t *config)
{
    return (config->aborted && *config->aborted) || !timing_before_deadline(&config->deadline);
}

static int parse_turn_credentials(const char *body, turn_credentials_t *credentials)
{
    json_value_t *root = json_parse(body ? body : "");
    if (!root) return -1;
    memset(credentials, 0, sizeof(*credentials));
    const char *value = json_get_string(root, "username");
    if (value) snprintf(credentials->username, sizeof(credentials->username), "%s", value);
    value = json_get_string(root, "credential");
    if (value) snprintf(credentials->credential, sizeof(credentials->credential), "%s", value);
    credentials->ttl_sec = json_get_int(root, "ttlSec", 0);
    json_value_t *servers = json_get(root, "servers");
    if (servers && servers->type == JSON_ARRAY) {
        for (json_element_t *element = servers->u.array;
             element && credentials->server_count < MAX_ICE_SERVERS;
             element = element->next) {
            if (element->value && element->value->type == JSON_STRING) {
                snprintf(credentials->servers[credentials->server_count], MAX_ICE_SERVER_LEN,
                         "%s", element->value->u.string);
                credentials->server_count++;
            }
        }
    }
    json_free(root);
    return credentials->server_count > 0 ? 0 : -1;
}

static int fetch_turn_credentials(const packet_loss_config_t *config,
                                  turn_credentials_t *credentials,
                                  char *error, size_t error_len)
{
    http_session_t session;
    http_session_init(&session, config->http);
    if (!session.easy) return ERR_MEMORY;
    http_response_t response;
    int result = http_get_json(&session, "/api/turn/credentials", CONTROL_BODY_LIMIT, &response);
    if (result != ERR_OK) {
        snprintf(error, error_len, "TURN credential request failed: %s", http_session_error(&session));
    } else if (response.status_code != 200) {
        snprintf(error, error_len, "TURN credential request returned HTTP %d", response.status_code);
        result = ERR_HTTP;
    } else if (strncasecmp(response.content_type, "application/json", 16) != 0) {
        snprintf(error, error_len, "TURN credential response was not JSON");
        result = ERR_PROTOCOL;
    } else if (parse_turn_credentials(response.body, credentials) != 0) {
        snprintf(error, error_len, "TURN credential response was incomplete");
        result = ERR_PARSE;
    }
    http_response_free(&response);
    http_session_cleanup(&session);
    return result;
}

static bool has_turn_server(const turn_credentials_t *credentials)
{
    for (int index = 0; index < credentials->server_count; index++) {
        if (strncasecmp(credentials->servers[index], "turn:", 5) == 0 ||
            strncasecmp(credentials->servers[index], "turns:", 6) == 0) {
            return true;
        }
    }
    return false;
}

static char *credential_uri(http_session_t *session, const char *server,
                            const char *username, const char *credential)
{
    if (strncasecmp(server, "turn:", 5) != 0 && strncasecmp(server, "turns:", 6) != 0) {
        return strdup(server);
    }
    size_t scheme_len = strncasecmp(server, "turns:", 6) == 0 ? 6 : 5;
    char *escaped_user = http_escape(session, username);
    char *escaped_credential = http_escape(session, credential);
    if (!escaped_user || !escaped_credential) {
        curl_free(escaped_user);
        curl_free(escaped_credential);
        return NULL;
    }
    size_t needed = strlen(server) + strlen(escaped_user) + strlen(escaped_credential) + 3;
    char *result = malloc(needed);
    if (result) {
        snprintf(result, needed, "%.*s%s:%s@%s", (int)scheme_len, server,
                 escaped_user, escaped_credential, server + scheme_len);
    }
    curl_free(escaped_user);
    curl_free(escaped_credential);
    return result;
}

static void RTC_API gathering_callback(int pc, rtcGatheringState state, void *opaque)
{
    (void)pc;
    rtc_state_t *rtc = opaque;
    pthread_mutex_lock(&rtc->mutex);
    if (state == RTC_GATHERING_COMPLETE) rtc->gathering_complete = true;
    pthread_mutex_unlock(&rtc->mutex);
}

static void RTC_API state_callback(int pc, rtcState state, void *opaque)
{
    (void)pc;
    rtc_state_t *rtc = opaque;
    pthread_mutex_lock(&rtc->mutex);
    if (state == RTC_FAILED || state == RTC_DISCONNECTED) rtc->failed = true;
    if (state == RTC_CLOSED) rtc->closed = true;
    pthread_mutex_unlock(&rtc->mutex);
}

static void RTC_API open_callback(int id, void *opaque)
{
    (void)id;
    rtc_state_t *rtc = opaque;
    pthread_mutex_lock(&rtc->mutex);
    rtc->open = true;
    pthread_mutex_unlock(&rtc->mutex);
}

static void RTC_API closed_callback(int id, void *opaque)
{
    (void)id;
    rtc_state_t *rtc = opaque;
    pthread_mutex_lock(&rtc->mutex);
    rtc->closed = true;
    pthread_mutex_unlock(&rtc->mutex);
}

static void RTC_API error_callback(int id, const char *message, void *opaque)
{
    (void)id;
    (void)message;
    rtc_state_t *rtc = opaque;
    pthread_mutex_lock(&rtc->mutex);
    rtc->failed = true;
    pthread_mutex_unlock(&rtc->mutex);
}

static void RTC_API message_callback(int id, const char *message, int size, void *opaque)
{
    (void)id;
    rtc_state_t *rtc = opaque;
    if (size != NETSPEED_PACKET_FRAME_SIZE || !message) return;
    bool acknowledgement = false;
    uint32_t sequence = 0;
    if (packet_frame_decode((const uint8_t *)message, (size_t)size, &acknowledgement,
                            &sequence, NULL, NULL) != 0 || !acknowledgement ||
        sequence >= PACKET_LOSS_COUNT) {
        return;
    }
    struct timespec now;
    timing_now(&now);
    pthread_mutex_lock(&rtc->mutex);
    if (!rtc->ack_seen[sequence] && rtc->sent_valid[sequence]) {
        double rtt_ms = timing_diff_ms(&rtc->sent_monotonic[sequence], &now);
        if (rtt_ms > 0 && rtt_ms < 30000) {
            rtc->ack_seen[sequence] = true;
            rtc->rtts[rtc->ack_count++] = rtt_ms;
        }
    }
    pthread_mutex_unlock(&rtc->mutex);
}

static bool wait_flag(const packet_loss_config_t *config, rtc_state_t *rtc,
                      bool *field, int timeout_ms)
{
    int64_t deadline = timing_monotonic_ms() + timeout_ms;
    while (timing_monotonic_ms() < deadline && !config_canceled(config)) {
        pthread_mutex_lock(&rtc->mutex);
        bool value = *field;
        bool failed = rtc->failed;
        pthread_mutex_unlock(&rtc->mutex);
        if (value) return true;
        if (failed) return false;
        timing_sleep_ms(10);
    }
    return false;
}

static char *local_description(int pc)
{
    int size = rtcGetLocalDescription(pc, NULL, 0);
    if (size <= 0) return NULL;
    char *description = malloc((size_t)size);
    if (!description) return NULL;
    if (rtcGetLocalDescription(pc, description, size) < 0) {
        free(description);
        return NULL;
    }
    return description;
}

static char *exchange_offer(const packet_loss_config_t *config, const char *sdp,
                            char *test_id, size_t test_id_len,
                            char *error, size_t error_len)
{
    json_writer_t writer;
    json_writer_init(&writer);
    json_start_object(&writer);
    json_kv_string(&writer, "sdp", sdp);
    json_kv_string(&writer, "type", "offer");
    json_kv_string(&writer, "testProfile", "loss-exact-v1");
    json_end_object(&writer);

    http_session_t session;
    http_session_init(&session, config->http);
    http_response_t response;
    int result = http_post_json(&session, "/api/packet-test/offer",
                                json_writer_string(&writer), CONTROL_BODY_LIMIT, &response);
    json_writer_free(&writer);
    char *answer = NULL;
    if (result != ERR_OK) {
        snprintf(error, error_len, "signaling request failed: %s", http_session_error(&session));
    } else if (response.status_code != 200) {
        snprintf(error, error_len, "signaling request returned HTTP %d", response.status_code);
    } else {
        json_value_t *root = json_parse(response.body ? response.body : "");
        const char *answer_sdp = root ? json_get_string(root, "sdp") : NULL;
        const char *type = root ? json_get_string(root, "type") : NULL;
        const char *id = root ? json_get_string(root, "testId") : NULL;
        if (!answer_sdp || !type || strcmp(type, "answer") != 0 || !id) {
            snprintf(error, error_len, "signaling response was incomplete");
        } else {
            answer = strdup(answer_sdp);
            snprintf(test_id, test_id_len, "%s", id);
        }
        json_free(root);
    }
    http_response_free(&response);
    http_session_cleanup(&session);
    return answer;
}

static int parse_report(const char *body, packet_report_t *report)
{
    json_value_t *root = json_parse(body ? body : "");
    if (!root) return -1;
    memset(report, 0, sizeof(*report));
    report->ok = json_get_bool(root, "ok", false);
    report->protocol_version = json_get_int(root, "protocolVersion", 0);
    report->frame_size_bytes = json_get_int(root, "frameSizeBytes", 0);
    report->forward_received = json_get_int(root, "forwardReceived", -1);
    report->acknowledgements_sent = json_get_int(root, "acknowledgementsSent", -1);
    report->duplicate_frames = json_get_int(root, "duplicateFrames", -1);
    report->invalid_frames = json_get_int(root, "invalidFrames", -1);
    report->ack_send_failures = json_get_int(root, "ackSendFailures", -1);
    json_free(root);
    return 0;
}

static int report_packet_test(const packet_loss_config_t *config, const char *test_id,
                              int sent, int received, const rtt_stats_t *rtt,
                              double jitter, packet_report_t *report,
                              char *error, size_t error_len)
{
    json_writer_t writer;
    json_writer_init(&writer);
    json_start_object(&writer);
    json_kv_string(&writer, "testId", test_id);
    json_kv_int(&writer, "sent", sent);
    json_kv_int(&writer, "received", received);
    json_kv_number(&writer, "lossPercent", loss_percent(sent, received));
    json_kv_number(&writer, "rttMinMs", rtt->min);
    json_kv_number(&writer, "rttMedianMs", rtt->median);
    json_kv_number(&writer, "rttP90Ms", rtt->p90);
    json_kv_number(&writer, "jitterMs", jitter);
    json_end_object(&writer);

    http_session_t session;
    http_session_init(&session, config->http);
    http_response_t response;
    int result = http_post_json(&session, "/api/packet-test/report",
                                json_writer_string(&writer), CONTROL_BODY_LIMIT, &response);
    json_writer_free(&writer);
    if (result != ERR_OK) {
        snprintf(error, error_len, "packet report failed: %s", http_session_error(&session));
    } else if (response.status_code != 200) {
        snprintf(error, error_len, "packet report returned HTTP %d", response.status_code);
        result = ERR_HTTP;
    } else if (parse_report(response.body, report) != 0) {
        snprintf(error, error_len, "packet report response was incomplete");
        result = ERR_PARSE;
    }
    http_response_free(&response);
    http_session_cleanup(&session);
    return result;
}

static int validate_report(const packet_report_t *report, int sent, int received,
                           char *error, size_t error_len)
{
    if (!report->ok) {
        snprintf(error, error_len, "server rejected packet report");
        return -1;
    }
    if (report->protocol_version < NETSPEED_MEASUREMENT_PROTOCOL_VERSION ||
        report->frame_size_bytes != NETSPEED_PACKET_FRAME_SIZE ||
        report->forward_received < 0 || report->forward_received > sent ||
        report->acknowledgements_sent < 0 ||
        report->acknowledgements_sent > report->forward_received ||
        report->ack_send_failures < 0 ||
        report->acknowledgements_sent + report->ack_send_failures != report->forward_received ||
        received < 0 || received > report->acknowledgements_sent ||
        report->duplicate_frames < 0 || report->invalid_frames < 0) {
        snprintf(error, error_len, "daemon packet counters were inconsistent");
        return -1;
    }
    return 0;
}

int packet_loss_run(const packet_loss_config_t *config, packet_loss_result_t *result,
                    char *error, size_t error_len)
{
    memset(result, 0, sizeof(*result));
    result->frame_size_bytes = NETSPEED_PACKET_FRAME_SIZE;
    if (config->server_frame_version < NETSPEED_PACKET_FRAME_VERSION) {
        unavailable(result, "server does not support exact-size packet-loss frames v1");
        return ERR_OK;
    }
    turn_credentials_t credentials;
    int status = fetch_turn_credentials(config, &credentials, error, error_len);
    if (status != ERR_OK) {
        unavailable(result, error);
        return ERR_OK;
    }
    if (!has_turn_server(&credentials)) {
        unavailable(result, "TURN relay is not configured");
        return ERR_OK;
    }

    http_session_t escape_session;
    http_session_init(&escape_session, config->http);
    char *ice_storage[MAX_ICE_SERVERS] = {0};
    const char *ice_servers[MAX_ICE_SERVERS] = {0};
    int ice_count = 0;
    for (int index = 0; index < credentials.server_count; index++) {
        char *uri = credential_uri(&escape_session, credentials.servers[index],
                                   credentials.username, credentials.credential);
        if (uri) {
            ice_storage[ice_count] = uri;
            ice_servers[ice_count++] = uri;
        }
    }
    http_session_cleanup(&escape_session);
    if (ice_count == 0) {
        unavailable(result, "failed to construct TURN server URLs");
        return ERR_OK;
    }

    rtc_state_t rtc;
    memset(&rtc, 0, sizeof(rtc));
    pthread_mutex_init(&rtc.mutex, NULL);
    rtc.pc = -1;
    rtc.dc = -1;
    rtcInitLogger(RTC_LOG_NONE, NULL);
    rtcConfiguration rtc_config;
    memset(&rtc_config, 0, sizeof(rtc_config));
    rtc_config.iceServers = ice_servers;
    rtc_config.iceServersCount = ice_count;
    rtc_config.iceTransportPolicy = RTC_TRANSPORT_POLICY_RELAY;
    rtc_config.disableAutoNegotiation = true;
    rtc_config.maxMessageSize = 64 * 1024;
    rtc.pc = rtcCreatePeerConnection(&rtc_config);
    for (int index = 0; index < ice_count; index++) free(ice_storage[index]);
    if (rtc.pc < 0) {
        pthread_mutex_destroy(&rtc.mutex);
        unavailable(result, "failed to create libdatachannel peer connection");
        return ERR_OK;
    }
    rtcSetUserPointer(rtc.pc, &rtc);
    rtcSetGatheringStateChangeCallback(rtc.pc, gathering_callback);
    rtcSetStateChangeCallback(rtc.pc, state_callback);

    rtcDataChannelInit channel_init;
    memset(&channel_init, 0, sizeof(channel_init));
    channel_init.reliability.unordered = true;
    channel_init.reliability.unreliable = true;
    channel_init.reliability.maxRetransmits = 0;
    rtc.dc = rtcCreateDataChannelEx(rtc.pc, "packet-loss", &channel_init);
    if (rtc.dc < 0) {
        rtcDeletePeerConnection(rtc.pc);
        pthread_mutex_destroy(&rtc.mutex);
        unavailable(result, "failed to create unreliable packet-loss data channel");
        return ERR_OK;
    }
    rtcSetUserPointer(rtc.dc, &rtc);
    rtcSetOpenCallback(rtc.dc, open_callback);
    rtcSetClosedCallback(rtc.dc, closed_callback);
    rtcSetErrorCallback(rtc.dc, error_callback);
    rtcSetMessageCallback(rtc.dc, message_callback);

    char reason[256] = {0};
    if (rtcSetLocalDescription(rtc.pc, "offer") < 0 ||
        !wait_flag(config, &rtc, &rtc.gathering_complete, 10000)) {
        snprintf(reason, sizeof(reason), "%s", "ICE gathering timeout");
        goto unavailable_cleanup;
    }
    char *offer = local_description(rtc.pc);
    if (!offer) {
        snprintf(reason, sizeof(reason), "%s", "failed to read local SDP offer");
        goto unavailable_cleanup;
    }
    char test_id[96] = {0};
    char *answer = exchange_offer(config, offer, test_id, sizeof(test_id), error, error_len);
    free(offer);
    if (!answer) {
        snprintf(reason, sizeof(reason), "signaling failed: %s", error);
        goto unavailable_cleanup;
    }
    if (rtcSetRemoteDescription(rtc.pc, answer, "answer") < 0) {
        free(answer);
        snprintf(reason, sizeof(reason), "%s", "failed to apply remote SDP answer");
        goto unavailable_cleanup;
    }
    free(answer);
    if (!wait_flag(config, &rtc, &rtc.open, 15000)) {
        snprintf(reason, sizeof(reason), "%s", "ICE/data-channel connection timeout");
        goto unavailable_cleanup;
    }

    int actual_sent = 0;
    for (int index = 0; index < PACKET_LOSS_COUNT && !config_canceled(config); index++) {
        pthread_mutex_lock(&rtc.mutex);
        bool failed = rtc.failed;
        pthread_mutex_unlock(&rtc.mutex);
        if (failed) break;
        uint32_t sequence = (uint32_t)actual_sent;
        uint8_t frame[NETSPEED_PACKET_FRAME_SIZE];
        int64_t sent_wall = timing_now_ms();
        struct timespec sent_mono;
        timing_now(&sent_mono);
        packet_frame_encode_probe(frame, sequence, sent_wall);
        pthread_mutex_lock(&rtc.mutex);
        rtc.sent_monotonic[sequence] = sent_mono;
        rtc.sent_valid[sequence] = true;
        pthread_mutex_unlock(&rtc.mutex);
        if (rtcSendMessage(rtc.dc, (const char *)frame, NETSPEED_PACKET_FRAME_SIZE) < 0) {
            pthread_mutex_lock(&rtc.mutex);
            rtc.sent_valid[sequence] = false;
            pthread_mutex_unlock(&rtc.mutex);
            continue;
        }
        actual_sent++;
        if (config->progress) config->progress("packet-loss", index + 1, PACKET_LOSS_COUNT, 0);
        timing_sleep_ms(PACKET_LOSS_INTERVAL_MS);
    }
    if (actual_sent == 0) {
        snprintf(reason, sizeof(reason), "%s", "no exact-size packet probes were sent");
        goto unavailable_cleanup;
    }
    int64_t drain_until = timing_monotonic_ms() + PACKET_LOSS_DRAIN_MS;
    while (timing_monotonic_ms() < drain_until && !config_canceled(config)) timing_sleep_ms(10);

    pthread_mutex_lock(&rtc.mutex);
    int ack_count = rtc.ack_count;
    double rtts[PACKET_LOSS_COUNT];
    memcpy(rtts, rtc.rtts, (size_t)ack_count * sizeof(*rtts));
    pthread_mutex_unlock(&rtc.mutex);
    rtt_stats_t rtt = {0};
    double jitter = 0;
    if (ack_count > 0) {
        rtt.min = stats_percentile(rtts, (size_t)ack_count, 0);
        rtt.median = stats_percentile(rtts, (size_t)ack_count, 50);
        rtt.p90 = stats_percentile(rtts, (size_t)ack_count, 90);
        jitter = stats_jitter(rtts, (size_t)ack_count);
    }
    packet_report_t report;
    status = report_packet_test(config, test_id, actual_sent, ack_count, &rtt, jitter,
                                &report, error, error_len);
    if (status != ERR_OK || validate_report(&report, actual_sent, ack_count, error, error_len) != 0) {
        snprintf(reason, sizeof(reason), "packet report failed: %s", error);
        goto unavailable_cleanup;
    }

    result->sent = actual_sent;
    result->received = ack_count;
    result->loss_percent = loss_percent(actual_sent, ack_count);
    result->transaction_loss_percent = result->loss_percent;
    result->forward_sent = actual_sent;
    result->forward_received = report.forward_received;
    result->forward_loss_percent = loss_percent(actual_sent, report.forward_received);
    result->forward_loss_available = true;
    result->acknowledgements_sent = report.acknowledgements_sent;
    result->acknowledgements_received = ack_count;
    result->reverse_acknowledgement_loss_percent = loss_percent(report.acknowledgements_sent, ack_count);
    result->reverse_loss_available = report.acknowledgements_sent > 0;
    result->duplicate_frames = report.duplicate_frames;
    result->invalid_frames = report.invalid_frames;
    result->ack_send_failures = report.ack_send_failures;
    result->rtt_stats_ms = rtt;
    result->jitter_ms = jitter;
    snprintf(result->test_id, sizeof(result->test_id), "%s", test_id);
    result->unavailable = false;

    rtcClose(rtc.dc);
    rtcDeleteDataChannel(rtc.dc);
    rtcClosePeerConnection(rtc.pc);
    rtcDeletePeerConnection(rtc.pc);
    pthread_mutex_destroy(&rtc.mutex);
    return ERR_OK;

unavailable_cleanup:
    unavailable(result, reason[0] ? reason : "packet-loss test unavailable");
    if (rtc.dc >= 0) {
        rtcClose(rtc.dc);
        rtcDeleteDataChannel(rtc.dc);
    }
    if (rtc.pc >= 0) {
        rtcClosePeerConnection(rtc.pc);
        rtcDeletePeerConnection(rtc.pc);
    }
    pthread_mutex_destroy(&rtc.mutex);
    return ERR_OK;
}
#endif
