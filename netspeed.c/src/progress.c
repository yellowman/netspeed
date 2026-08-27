#include "progress.h"
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <time.h>
#include <unistd.h>
static int forced = -1;
static struct timespec started;
static int initialized;
void ns_progress_force(int enabled) { forced = enabled ? 1 : 0; }
int ns_progress_enabled(void) {
    const char *v;
    if (forced >= 0) return forced;
    v = getenv("NETSPEED_PROGRESS");
    if (v) return strcmp(v,"0") && strcasecmp(v,"false") && strcasecmp(v,"no");
    return isatty(STDERR_FILENO);
}
void ns_progress(const char *format, ...) {
    struct timespec now; double elapsed; va_list ap;
    if (!ns_progress_enabled()) return;
    if (!initialized) { clock_gettime(CLOCK_MONOTONIC, &started); initialized=1; }
    clock_gettime(CLOCK_MONOTONIC, &now);
    elapsed=(double)(now.tv_sec-started.tv_sec)+(double)(now.tv_nsec-started.tv_nsec)/1000000000.0;
    fprintf(stderr,"[%6.1fs] ",elapsed);
    va_start(ap,format); vfprintf(stderr,format,ap); va_end(ap);
    fputc('\n',stderr); fflush(stderr);
}
