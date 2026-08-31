/*
 * measurement_transport.h - Negotiated HTTP measurement transport for C.
 */
#ifndef NETSPEED_MEASUREMENT_TRANSPORT_H
#define NETSPEED_MEASUREMENT_TRANSPORT_H

#include "http.h"
#include "types.h"

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void measurement_selection_legacy(measurement_selection_t *selection);

int measurement_negotiate(const measurement_capabilities_t *capabilities,
                          const config_t *config,
                          measurement_selection_t *selection,
                          char *error, size_t error_capacity);

int measurement_build_download_path(http_session_t *session,
                                    const measurement_selection_t *selection,
                                    int64_t bytes, const char *profile, int run,
                                    const char *condition,
                                    char *path, size_t path_capacity);

int measurement_build_upload_path(http_session_t *session,
                                  const measurement_selection_t *selection,
                                  int64_t bytes, const char *profile, int run,
                                  char *path, size_t path_capacity);

int measurement_build_latency_path(http_session_t *session,
                                   const measurement_selection_t *selection,
                                   const char *condition, int sequence, int attempt,
                                   char *path, size_t path_capacity);

int measurement_verify_download(const measurement_selection_t *selection,
                                const http_response_t *response,
                                int64_t expected_bytes,
                                const char *expected_measurement,
                                char *error, size_t error_capacity);

int measurement_verify_upload(const measurement_selection_t *selection,
                              const http_response_t *response,
                              int64_t expected_bytes,
                              char *error, size_t error_capacity);

int measurement_verify_latency(const measurement_selection_t *selection,
                               const http_response_t *response,
                               char *error, size_t error_capacity);

#ifdef __cplusplus
}
#endif

#endif /* NETSPEED_MEASUREMENT_TRANSPORT_H */
