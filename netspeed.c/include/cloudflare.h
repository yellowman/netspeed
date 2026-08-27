#ifndef NETSPEED_CLOUDFLARE_H
#define NETSPEED_CLOUDFLARE_H

/* Returns -1 when the ordinary strict Netspeed client should continue. */
int ns_cloudflare_dispatch(int *argc, char ***argv);

#endif
