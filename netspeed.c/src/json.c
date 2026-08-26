/*
 * json.c - Minimal JSON parsing and generation
 *
 * Simple recursive descent parser and string builder.
 */

#include "json.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <math.h>

/* Initial buffer size for writer */
#define INITIAL_BUF_SIZE 4096

/* ===================== Parser ===================== */

/* Parser state */
typedef struct {
    const char *src;
    size_t pos;
    size_t len;
} parser_t;

static void skip_whitespace(parser_t *p)
{
    while (p->pos < p->len && isspace((unsigned char)p->src[p->pos])) {
        p->pos++;
    }
}

static char peek(parser_t *p)
{
    if (p->pos >= p->len) return '\0';
    return p->src[p->pos];
}

static char consume(parser_t *p)
{
    if (p->pos >= p->len) return '\0';
    return p->src[p->pos++];
}

static bool expect(parser_t *p, char c)
{
    skip_whitespace(p);
    if (peek(p) == c) {
        consume(p);
        return true;
    }
    return false;
}

/* Forward declarations */
static json_value_t *parse_value(parser_t *p);

static json_value_t *alloc_value(json_type_t type)
{
    json_value_t *v = calloc(1, sizeof(*v));
    if (v) v->type = type;
    return v;
}

static json_value_t *parse_null(parser_t *p)
{
    if (strncmp(p->src + p->pos, "null", 4) == 0) {
        p->pos += 4;
        return alloc_value(JSON_NULL);
    }
    return NULL;
}

static json_value_t *parse_bool(parser_t *p)
{
    if (strncmp(p->src + p->pos, "true", 4) == 0) {
        p->pos += 4;
        json_value_t *v = alloc_value(JSON_BOOL);
        if (v) v->u.boolean = true;
        return v;
    }
    if (strncmp(p->src + p->pos, "false", 5) == 0) {
        p->pos += 5;
        json_value_t *v = alloc_value(JSON_BOOL);
        if (v) v->u.boolean = false;
        return v;
    }
    return NULL;
}

static json_value_t *parse_number(parser_t *p)
{
    const char *start = p->src + p->pos;
    char *end;
    double num = strtod(start, &end);

    if (end == start) return NULL;

    p->pos += (end - start);
    json_value_t *v = alloc_value(JSON_NUMBER);
    if (v) v->u.number = num;
    return v;
}

static char *parse_string_raw(parser_t *p)
{
    if (peek(p) != '"') return NULL;
    consume(p);

    size_t start = p->pos;
    size_t len = 0;

    /* First pass: count length */
    while (p->pos < p->len) {
        char c = p->src[p->pos];
        if (c == '"') break;
        if (c == '\\') {
            p->pos++;
            if (p->pos < p->len) p->pos++;
            len++;
        } else {
            p->pos++;
            len++;
        }
    }

    /* Allocate string */
    char *str = malloc(len + 1);
    if (!str) return NULL;

    /* Second pass: copy with escape handling */
    p->pos = start;
    size_t i = 0;
    while (p->pos < p->len) {
        char c = consume(p);
        if (c == '"') break;
        if (c == '\\') {
            char esc = consume(p);
            switch (esc) {
            case 'n': str[i++] = '\n'; break;
            case 't': str[i++] = '\t'; break;
            case 'r': str[i++] = '\r'; break;
            case '\\': str[i++] = '\\'; break;
            case '"': str[i++] = '"'; break;
            case '/': str[i++] = '/'; break;
            case 'u':
                /* Skip unicode escape (simplified) */
                for (int j = 0; j < 4 && p->pos < p->len; j++) consume(p);
                str[i++] = '?';
                break;
            default: str[i++] = esc; break;
            }
        } else {
            str[i++] = c;
        }
    }
    str[i] = '\0';

    /* Skip closing quote if not consumed */
    if (peek(p) == '"') consume(p);

    return str;
}

static json_value_t *parse_string(parser_t *p)
{
    char *str = parse_string_raw(p);
    if (!str) return NULL;

    json_value_t *v = alloc_value(JSON_STRING);
    if (!v) {
        free(str);
        return NULL;
    }
    v->u.string = str;
    return v;
}

static json_value_t *parse_array(parser_t *p)
{
    if (!expect(p, '[')) return NULL;

    json_value_t *arr = alloc_value(JSON_ARRAY);
    if (!arr) return NULL;
    arr->u.array = NULL;

    json_element_t *tail = NULL;

    skip_whitespace(p);
    if (peek(p) == ']') {
        consume(p);
        return arr;
    }

    while (1) {
        json_value_t *elem = parse_value(p);
        if (!elem) {
            json_free(arr);
            return NULL;
        }

        json_element_t *node = calloc(1, sizeof(*node));
        if (!node) {
            json_free(elem);
            json_free(arr);
            return NULL;
        }
        node->value = elem;
        node->next = NULL;

        if (tail) {
            tail->next = node;
        } else {
            arr->u.array = node;
        }
        tail = node;

        skip_whitespace(p);
        if (peek(p) == ',') {
            consume(p);
            skip_whitespace(p);
        } else if (peek(p) == ']') {
            consume(p);
            break;
        } else {
            json_free(arr);
            return NULL;
        }
    }

    return arr;
}

static json_value_t *parse_object(parser_t *p)
{
    if (!expect(p, '{')) return NULL;

    json_value_t *obj = alloc_value(JSON_OBJECT);
    if (!obj) return NULL;
    obj->u.object = NULL;

    json_member_t *tail = NULL;

    skip_whitespace(p);
    if (peek(p) == '}') {
        consume(p);
        return obj;
    }

    while (1) {
        skip_whitespace(p);
        char *key = parse_string_raw(p);
        if (!key) {
            json_free(obj);
            return NULL;
        }

        skip_whitespace(p);
        if (!expect(p, ':')) {
            free(key);
            json_free(obj);
            return NULL;
        }

        json_value_t *val = parse_value(p);
        if (!val) {
            free(key);
            json_free(obj);
            return NULL;
        }

        json_member_t *member = calloc(1, sizeof(*member));
        if (!member) {
            free(key);
            json_free(val);
            json_free(obj);
            return NULL;
        }
        member->key = key;
        member->value = val;
        member->next = NULL;

        if (tail) {
            tail->next = member;
        } else {
            obj->u.object = member;
        }
        tail = member;

        skip_whitespace(p);
        if (peek(p) == ',') {
            consume(p);
        } else if (peek(p) == '}') {
            consume(p);
            break;
        } else {
            json_free(obj);
            return NULL;
        }
    }

    return obj;
}

static json_value_t *parse_value(parser_t *p)
{
    skip_whitespace(p);

    char c = peek(p);
    if (c == 'n') return parse_null(p);
    if (c == 't' || c == 'f') return parse_bool(p);
    if (c == '"') return parse_string(p);
    if (c == '[') return parse_array(p);
    if (c == '{') return parse_object(p);
    if (c == '-' || isdigit((unsigned char)c)) return parse_number(p);

    return NULL;
}

json_value_t *json_parse(const char *str)
{
    if (!str) return NULL;

    parser_t p = {
        .src = str,
        .pos = 0,
        .len = strlen(str),
    };

    return parse_value(&p);
}

void json_free(json_value_t *val)
{
    if (!val) return;

    switch (val->type) {
    case JSON_STRING:
        free(val->u.string);
        break;
    case JSON_ARRAY:
        {
            json_element_t *elem = val->u.array;
            while (elem) {
                json_element_t *next = elem->next;
                json_free(elem->value);
                free(elem);
                elem = next;
            }
        }
        break;
    case JSON_OBJECT:
        {
            json_member_t *member = val->u.object;
            while (member) {
                json_member_t *next = member->next;
                free(member->key);
                json_free(member->value);
                free(member);
                member = next;
            }
        }
        break;
    default:
        break;
    }

    free(val);
}

json_value_t *json_get(json_value_t *obj, const char *key)
{
    if (!obj || obj->type != JSON_OBJECT || !key) return NULL;

    for (json_member_t *m = obj->u.object; m; m = m->next) {
        if (strcmp(m->key, key) == 0) {
            return m->value;
        }
    }
    return NULL;
}

const char *json_get_string(json_value_t *obj, const char *key)
{
    json_value_t *v = json_get(obj, key);
    if (!v || v->type != JSON_STRING) return NULL;
    return v->u.string;
}

double json_get_number(json_value_t *obj, const char *key, double default_val)
{
    json_value_t *v = json_get(obj, key);
    if (!v || v->type != JSON_NUMBER) return default_val;
    return v->u.number;
}

int json_get_int(json_value_t *obj, const char *key, int default_val)
{
    json_value_t *v = json_get(obj, key);
    if (!v || v->type != JSON_NUMBER) return default_val;
    return (int)v->u.number;
}

bool json_get_bool(json_value_t *obj, const char *key, bool default_val)
{
    json_value_t *v = json_get(obj, key);
    if (!v || v->type != JSON_BOOL) return default_val;
    return v->u.boolean;
}

/* ===================== Writer ===================== */

static bool writer_grow(json_writer_t *w, size_t need)
{
    if (w->len + need + 1 > w->cap) {
        size_t new_cap = w->cap * 2;
        if (new_cap < w->len + need + 1) {
            new_cap = w->len + need + 1;
        }
        char *new_buf = realloc(w->buf, new_cap);
        if (!new_buf) {
            return false; /* Keep original buffer intact */
        }
        w->buf = new_buf;
        w->cap = new_cap;
    }
    return true;
}

static void writer_append(json_writer_t *w, const char *str)
{
    size_t len = strlen(str);
    if (!writer_grow(w, len) || !w->buf) {
        return; /* Silently fail if allocation failed */
    }
    memcpy(w->buf + w->len, str, len);
    w->len += len;
    w->buf[w->len] = '\0';
}

static void writer_append_char(json_writer_t *w, char c)
{
    if (!writer_grow(w, 1) || !w->buf) {
        return; /* Silently fail if allocation failed */
    }
    w->buf[w->len++] = c;
    w->buf[w->len] = '\0';
}

static void maybe_comma(json_writer_t *w)
{
    if (w->need_comma) {
        writer_append_char(w, ',');
    }
    w->need_comma = false;
}

void json_writer_init(json_writer_t *w)
{
    w->buf = malloc(INITIAL_BUF_SIZE);
    if (w->buf) {
        w->buf[0] = '\0';
        w->cap = INITIAL_BUF_SIZE;
    } else {
        w->cap = 0;
    }
    w->len = 0;
    w->depth = 0;
    w->need_comma = false;
}

void json_writer_free(json_writer_t *w)
{
    free(w->buf);
    w->buf = NULL;
    w->len = 0;
    w->cap = 0;
}

const char *json_writer_string(json_writer_t *w)
{
    return w->buf;
}

void json_start_object(json_writer_t *w)
{
    maybe_comma(w);
    writer_append_char(w, '{');
    w->depth++;
    w->need_comma = false;
}

void json_end_object(json_writer_t *w)
{
    writer_append_char(w, '}');
    w->depth--;
    w->need_comma = true;
}

void json_start_array(json_writer_t *w)
{
    maybe_comma(w);
    writer_append_char(w, '[');
    w->depth++;
    w->need_comma = false;
}

void json_end_array(json_writer_t *w)
{
    writer_append_char(w, ']');
    w->depth--;
    w->need_comma = true;
}

void json_key(json_writer_t *w, const char *key)
{
    maybe_comma(w);
    writer_append_char(w, '"');
    writer_append(w, key);
    writer_append(w, "\":");
    w->need_comma = false;
}

void json_string(json_writer_t *w, const char *val)
{
    maybe_comma(w);
    writer_append_char(w, '"');

    /* Escape special characters */
    for (const char *p = val; *p; p++) {
        switch (*p) {
        case '"': writer_append(w, "\\\""); break;
        case '\\': writer_append(w, "\\\\"); break;
        case '\n': writer_append(w, "\\n"); break;
        case '\r': writer_append(w, "\\r"); break;
        case '\t': writer_append(w, "\\t"); break;
        default:
            if ((unsigned char)*p < 0x20) {
                char buf[8];
                snprintf(buf, sizeof(buf), "\\u%04x", (unsigned char)*p);
                writer_append(w, buf);
            } else {
                writer_append_char(w, *p);
            }
            break;
        }
    }

    writer_append_char(w, '"');
    w->need_comma = true;
}

void json_number(json_writer_t *w, double val)
{
    maybe_comma(w);
    char buf[64];

    if (isnan(val) || isinf(val)) {
        writer_append(w, "null");
    } else {
        /* Use %g for compact representation */
        snprintf(buf, sizeof(buf), "%.15g", val);
        writer_append(w, buf);
    }

    w->need_comma = true;
}

void json_int(json_writer_t *w, int64_t val)
{
    maybe_comma(w);
    char buf[32];
    snprintf(buf, sizeof(buf), "%lld", (long long)val);
    writer_append(w, buf);
    w->need_comma = true;
}

void json_bool(json_writer_t *w, bool val)
{
    maybe_comma(w);
    writer_append(w, val ? "true" : "false");
    w->need_comma = true;
}

void json_null(json_writer_t *w)
{
    maybe_comma(w);
    writer_append(w, "null");
    w->need_comma = true;
}

void json_kv_string(json_writer_t *w, const char *key, const char *val)
{
    json_key(w, key);
    json_string(w, val);
}

void json_kv_number(json_writer_t *w, const char *key, double val)
{
    json_key(w, key);
    json_number(w, val);
}

void json_kv_int(json_writer_t *w, const char *key, int64_t val)
{
    json_key(w, key);
    json_int(w, val);
}

void json_kv_bool(json_writer_t *w, const char *key, bool val)
{
    json_key(w, key);
    json_bool(w, val);
}

/* ===================== Results Serialization ===================== */

static void write_meta(json_writer_t *w, const meta_t *meta)
{
    json_start_object(w);
    json_kv_string(w, "hostname", meta->hostname);
    json_kv_string(w, "clientIp", meta->client_ip);
    json_kv_string(w, "httpProtocol", meta->http_protocol);
    json_kv_int(w, "asn", meta->asn);
    json_kv_string(w, "asOrganization", meta->as_organization);
    json_kv_string(w, "colo", meta->colo);
    json_kv_string(w, "country", meta->country);
    json_kv_string(w, "city", meta->city);
    json_kv_string(w, "region", meta->region);
    json_kv_string(w, "postalCode", meta->postal_code);
    json_kv_number(w, "latitude", meta->latitude);
    json_kv_number(w, "longitude", meta->longitude);
    if (meta->timezone[0]) {
        json_kv_string(w, "timezone", meta->timezone);
    }
    json_kv_int(w, "maxTransferBytes", meta->max_transfer_bytes);
    json_kv_int(w, "maxConcurrentTransfersPerClient", meta->max_concurrent_transfers_per_client);
    json_kv_int(w, "measurementProtocolVersion", meta->measurement_protocol_version);
    json_kv_int(w, "uploadReceiptVersion", meta->upload_receipt_version);
    json_kv_int(w, "packetLossFrameVersion", meta->packet_loss_frame_version);
    json_end_object(w);
}

static void write_summary(json_writer_t *w, const summary_t *summary)
{
    json_start_object(w);
    json_kv_number(w, "downloadMbps", summary->download_mbps);
    json_kv_number(w, "uploadMbps", summary->upload_mbps);
    json_kv_number(w, "latencyUnloadedMs", summary->latency_unloaded_ms);
    json_kv_number(w, "latencyDownloadMs", summary->latency_download_ms);
    json_kv_number(w, "latencyUploadMs", summary->latency_upload_ms);
    json_kv_number(w, "jitterMs", summary->jitter_ms);
    json_key(w, "packetLossPercent");
    if (summary->packet_loss_available) {
        json_number(w, summary->packet_loss_percent);
    } else {
        json_null(w);
    }
    json_end_object(w);
}

static void write_quality(json_writer_t *w, const quality_t *quality)
{
    json_start_object(w);
    json_kv_string(w, "videoStreaming", quality->video_streaming);
    json_kv_string(w, "gaming", quality->gaming);
    json_kv_string(w, "videoChatting", quality->video_chatting);
    json_end_object(w);
}

static void write_confidence(json_writer_t *w, const test_confidence_t *confidence)
{
    json_start_object(w);
    json_kv_string(w, "overall", confidence->overall);
    json_kv_int(w, "overallScore", confidence->overall_score);
    json_key(w, "metrics");
    json_start_object(w);
    json_key(w, "sampleCount");
    json_start_object(w);
    json_kv_int(w, "downloadWindows", confidence->sample_count.download_windows);
    json_kv_int(w, "uploadWindows", confidence->sample_count.upload_windows);
    json_kv_int(w, "unloadedLatency", confidence->sample_count.unloaded_latency);
    json_kv_int(w, "downloadLoadedLatency", confidence->sample_count.download_loaded_latency);
    json_kv_int(w, "uploadLoadedLatency", confidence->sample_count.upload_loaded_latency);
    json_kv_bool(w, "adequate", confidence->sample_count.adequate);
    json_end_object(w);
    json_key(w, "coefficientOfVariation");
    json_start_object(w);
    json_kv_number(w, "download", confidence->variability.download);
    json_kv_number(w, "upload", confidence->variability.upload);
    json_kv_number(w, "latency", confidence->variability.latency);
    json_kv_bool(w, "acceptable", confidence->variability.acceptable);
    json_end_object(w);
    json_key(w, "loadedOverlap");
    json_start_object(w);
    json_kv_int(w, "downloadAccepted", confidence->loaded_overlap.download_accepted);
    json_kv_int(w, "uploadAccepted", confidence->loaded_overlap.upload_accepted);
    json_kv_bool(w, "complete", confidence->loaded_overlap.complete);
    json_end_object(w);
    json_key(w, "timingAccuracy");
    json_start_object(w);
    json_kv_bool(w, "accurate", confidence->timing_accurate);
    json_end_object(w);
    json_key(w, "packetTest");
    json_start_object(w);
    json_kv_bool(w, "completed", confidence->packet_test_completed);
    json_end_object(w);
    json_end_object(w);
    json_key(w, "warnings");
    json_start_array(w);
    for (int index = 0; index < confidence->warning_count; index++) {
        json_string(w, confidence->warnings[index]);
    }
    json_end_array(w);
    json_end_object(w);
}

static void write_packet_loss(json_writer_t *w, const results_t *results)
{
    if (!results->packet_loss_present) {
        json_null(w);
        return;
    }
    const packet_loss_result_t *packet = &results->packet_loss;
    json_start_object(w);
    json_kv_int(w, "sent", packet->sent);
    json_kv_int(w, "received", packet->received);
    json_key(w, "lossPercent");
    if (packet->unavailable) json_null(w); else json_number(w, packet->loss_percent);
    json_key(w, "transactionLossPercent");
    if (packet->unavailable) json_null(w); else json_number(w, packet->transaction_loss_percent);
    json_kv_int(w, "forwardSent", packet->forward_sent);
    json_kv_int(w, "forwardReceived", packet->forward_received);
    json_key(w, "forwardLossPercent");
    if (!packet->unavailable && packet->forward_loss_available) json_number(w, packet->forward_loss_percent); else json_null(w);
    json_kv_int(w, "acknowledgementsSent", packet->acknowledgements_sent);
    json_kv_int(w, "acknowledgementsReceived", packet->acknowledgements_received);
    json_key(w, "reverseAcknowledgementLossPercent");
    if (!packet->unavailable && packet->reverse_loss_available) json_number(w, packet->reverse_acknowledgement_loss_percent); else json_null(w);
    json_kv_int(w, "frameSizeBytes", packet->frame_size_bytes);
    json_kv_int(w, "duplicateFrames", packet->duplicate_frames);
    json_kv_int(w, "invalidFrames", packet->invalid_frames);
    json_kv_int(w, "ackSendFailures", packet->ack_send_failures);
    json_key(w, "rttStatsMs");
    json_start_object(w);
    json_kv_number(w, "min", packet->rtt_stats_ms.min);
    json_kv_number(w, "median", packet->rtt_stats_ms.median);
    json_kv_number(w, "p90", packet->rtt_stats_ms.p90);
    json_end_object(w);
    json_kv_number(w, "jitterMs", packet->jitter_ms);
    if (packet->test_id[0]) json_kv_string(w, "testId", packet->test_id);
    if (packet->unavailable) {
        json_kv_bool(w, "unavailable", true);
        json_kv_string(w, "reason", packet->reason);
    }
    json_end_object(w);
}

char *results_to_json(const results_t *results)
{
    json_writer_t writer;
    json_writer_init(&writer);
    if (!writer.buf) return NULL;
    json_start_object(&writer);
    json_key(&writer, "meta");
    write_meta(&writer, &results->meta);
    json_key(&writer, "summary");
    write_summary(&writer, &results->summary);
    json_key(&writer, "quality");
    write_quality(&writer, &results->quality);
    json_key(&writer, "testConfidence");
    write_confidence(&writer, &results->test_confidence);

    json_key(&writer, "throughputSamples");
    json_start_array(&writer);
    for (int index = 0; index < results->throughput_count; index++) {
        const throughput_sample_t *sample = &results->throughput_samples[index];
        json_start_object(&writer);
        json_kv_int(&writer, "ts", sample->ts);
        json_kv_string(&writer, "direction", sample->direction);
        json_kv_int(&writer, "sizeBytes", sample->size_bytes);
        json_kv_number(&writer, "durationMs", sample->duration_ms);
        json_kv_number(&writer, "mbps", sample->mbps);
        json_kv_string(&writer, "profile", sample->profile);
        json_kv_int(&writer, "runIndex", sample->run_index);
        if (sample->sample_kind[0]) json_kv_string(&writer, "sampleKind", sample->sample_kind);
        if (sample->has_window_index) json_kv_int(&writer, "windowIndex", sample->window_index);
        if (sample->concurrency) json_kv_int(&writer, "concurrency", sample->concurrency);
        if (sample->chunk_bytes) json_kv_int(&writer, "chunkBytes", sample->chunk_bytes);
        if (sample->request_count) json_kv_int(&writer, "requestCount", sample->request_count);
        if (sample->timing_source[0]) json_kv_string(&writer, "timingSource", sample->timing_source);
        json_end_object(&writer);
    }
    json_end_array(&writer);

    json_key(&writer, "latencySamples");
    json_start_array(&writer);
    for (int index = 0; index < results->latency_count; index++) {
        const latency_sample_t *sample = &results->latency_samples[index];
        json_start_object(&writer);
        json_kv_int(&writer, "ts", sample->ts);
        if (sample->started_at) json_kv_int(&writer, "startedAt", sample->started_at);
        if (sample->ended_at) json_kv_int(&writer, "endedAt", sample->ended_at);
        json_kv_number(&writer, "rttMs", sample->rtt_ms);
        json_kv_string(&writer, "condition", sample->condition);
        if (sample->load_overlapped) json_kv_bool(&writer, "loadOverlapped", true);
        if (sample->load_tracking_accurate) json_kv_bool(&writer, "loadTrackingAccurate", true);
        if (sample->timing_source[0]) json_kv_string(&writer, "timingSource", sample->timing_source);
        json_end_object(&writer);
    }
    json_end_array(&writer);

    json_key(&writer, "packetLoss");
    write_packet_loss(&writer, results);
    json_kv_string(&writer, "startTime", results->start_time_rfc3339);
    json_kv_string(&writer, "endTime", results->end_time_rfc3339);
    json_end_object(&writer);
    char *output = writer.buf;
    writer.buf = NULL;
    return output;
}

int meta_from_json(const char *json_string, meta_t *meta)
{
    json_value_t *root = json_parse(json_string ? json_string : "");
    if (!root) return -1;
    memset(meta, 0, sizeof(*meta));
    const char *value;
#define COPY_STRING(field, key) do { \
    value = json_get_string(root, key); \
    if (value) snprintf(meta->field, sizeof(meta->field), "%s", value); \
} while (0)
    COPY_STRING(hostname, "hostname");
    COPY_STRING(client_ip, "clientIp");
    COPY_STRING(http_protocol, "httpProtocol");
    meta->asn = json_get_int(root, "asn", 0);
    COPY_STRING(as_organization, "asOrganization");
    COPY_STRING(colo, "colo");
    COPY_STRING(country, "country");
    COPY_STRING(city, "city");
    COPY_STRING(region, "region");
    COPY_STRING(postal_code, "postalCode");
    meta->latitude = json_get_number(root, "latitude", 0);
    meta->longitude = json_get_number(root, "longitude", 0);
    COPY_STRING(timezone, "timezone");
#undef COPY_STRING
    meta->max_transfer_bytes = (int64_t)json_get_number(root, "maxTransferBytes", 0);
    meta->max_concurrent_transfers_per_client = json_get_int(root, "maxConcurrentTransfersPerClient", 0);
    meta->measurement_protocol_version = json_get_int(root, "measurementProtocolVersion", 0);
    meta->upload_receipt_version = json_get_int(root, "uploadReceiptVersion", 0);
    meta->packet_loss_frame_version = json_get_int(root, "packetLossFrameVersion", 0);
    json_free(root);
    return 0;
}
