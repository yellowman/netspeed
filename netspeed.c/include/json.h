/*
 * json.h - Minimal JSON parsing and generation
 *
 * Simple JSON implementation without external dependencies.
 * Only supports the subset needed for speed test API.
 */

#ifndef NETSPEED_JSON_H
#define NETSPEED_JSON_H

#include "types.h"
#include <stdio.h>

/* JSON value types */
typedef enum {
    JSON_NULL,
    JSON_BOOL,
    JSON_NUMBER,
    JSON_STRING,
    JSON_ARRAY,
    JSON_OBJECT,
} json_type_t;

/* Forward declaration */
struct json_value;

/* JSON object member */
typedef struct json_member {
    char *key;
    struct json_value *value;
    struct json_member *next;
} json_member_t;

/* JSON array element */
typedef struct json_element {
    struct json_value *value;
    struct json_element *next;
} json_element_t;

/* JSON value */
typedef struct json_value {
    json_type_t type;
    union {
        bool boolean;
        double number;
        char *string;
        json_element_t *array;
        json_member_t *object;
    } u;
} json_value_t;

/*
 * Parse JSON string.
 * Returns NULL on error.
 * Caller must free with json_free().
 */
json_value_t *json_parse(const char *str);

/*
 * Free JSON value tree.
 */
void json_free(json_value_t *val);

/*
 * Get object member by key.
 * Returns NULL if not found or not an object.
 */
json_value_t *json_get(json_value_t *obj, const char *key);

/*
 * Get string value from object by key.
 * Returns NULL if not found or not a string.
 */
const char *json_get_string(json_value_t *obj, const char *key);

/*
 * Get number value from object by key.
 * Returns default_val if not found or not a number.
 */
double json_get_number(json_value_t *obj, const char *key, double default_val);

/*
 * Get integer value from object by key.
 * Returns default_val if not found or not a number.
 */
int json_get_int(json_value_t *obj, const char *key, int default_val);

/*
 * Get boolean value from object by key.
 * Returns default_val if not found or not a boolean.
 */
bool json_get_bool(json_value_t *obj, const char *key, bool default_val);

/* ===================== JSON Generation ===================== */

/* JSON writer context */
typedef struct {
    char *buf;
    size_t len;
    size_t cap;
    int depth;
    bool need_comma;
} json_writer_t;

/*
 * Initialize JSON writer.
 */
void json_writer_init(json_writer_t *w);

/*
 * Free JSON writer buffer.
 */
void json_writer_free(json_writer_t *w);

/*
 * Get resulting JSON string (null-terminated).
 * Valid until json_writer_free() is called.
 */
const char *json_writer_string(json_writer_t *w);

/*
 * Start object { ... }
 */
void json_start_object(json_writer_t *w);

/*
 * End object
 */
void json_end_object(json_writer_t *w);

/*
 * Start array [ ... ]
 */
void json_start_array(json_writer_t *w);

/*
 * End array
 */
void json_end_array(json_writer_t *w);

/*
 * Write object key (call before writing value).
 */
void json_key(json_writer_t *w, const char *key);

/*
 * Write string value.
 */
void json_string(json_writer_t *w, const char *val);

/*
 * Write number value.
 */
void json_number(json_writer_t *w, double val);

/*
 * Write integer value.
 */
void json_int(json_writer_t *w, int64_t val);

/*
 * Write boolean value.
 */
void json_bool(json_writer_t *w, bool val);

/*
 * Write null value.
 */
void json_null(json_writer_t *w);

/*
 * Convenience: write key-string pair.
 */
void json_kv_string(json_writer_t *w, const char *key, const char *val);

/*
 * Convenience: write key-number pair.
 */
void json_kv_number(json_writer_t *w, const char *key, double val);

/*
 * Convenience: write key-integer pair.
 */
void json_kv_int(json_writer_t *w, const char *key, int64_t val);

/*
 * Convenience: write key-boolean pair.
 */
void json_kv_bool(json_writer_t *w, const char *key, bool val);

/* ===================== Results Serialization ===================== */

/*
 * Serialize results to JSON string (matches web client format).
 * Caller must free returned string.
 */
char *results_to_json(const results_t *r);

/*
 * Parse /meta response into meta_t.
 * Returns 0 on success, -1 on error.
 */
int meta_from_json(const char *json_str, meta_t *meta);

#endif /* NETSPEED_JSON_H */
