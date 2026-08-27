#ifndef NETSPEED_PROGRESS_H
#define NETSPEED_PROGRESS_H
void ns_progress_force(int enabled);
int ns_progress_enabled(void);
void ns_progress(const char *format, ...);
#endif
