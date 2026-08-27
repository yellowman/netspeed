#ifndef NETSPEED_TEST_FAKE_RTC_H
#define NETSPEED_TEST_FAKE_RTC_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define RTC_API

typedef enum {
    RTC_NEW = 0,
    RTC_CONNECTING = 1,
    RTC_CONNECTED = 2,
    RTC_DISCONNECTED = 3,
    RTC_FAILED = 4,
    RTC_CLOSED = 5
} rtcState;

typedef enum {
    RTC_GATHERING_NEW = 0,
    RTC_GATHERING_INPROGRESS = 1,
    RTC_GATHERING_COMPLETE = 2
} rtcGatheringState;

typedef enum {
    RTC_LOG_NONE = 0,
    RTC_LOG_FATAL = 1,
    RTC_LOG_ERROR = 2,
    RTC_LOG_WARNING = 3,
    RTC_LOG_INFO = 4,
    RTC_LOG_DEBUG = 5,
    RTC_LOG_VERBOSE = 6
} rtcLogLevel;

typedef enum {
    RTC_TRANSPORT_POLICY_ALL = 0,
    RTC_TRANSPORT_POLICY_RELAY = 1
} rtcTransportPolicy;

typedef enum {
    RTC_CERTIFICATE_DEFAULT = 0,
    RTC_CERTIFICATE_ECDSA = 1,
    RTC_CERTIFICATE_RSA = 2
} rtcCertificateType;

typedef void (RTC_API *rtcLogCallbackFunc)(rtcLogLevel level, const char *message);
typedef void (RTC_API *rtcStateChangeCallbackFunc)(int pc, rtcState state, void *ptr);
typedef void (RTC_API *rtcGatheringStateCallbackFunc)(int pc, rtcGatheringState state, void *ptr);
typedef void (RTC_API *rtcOpenCallbackFunc)(int id, void *ptr);
typedef void (RTC_API *rtcClosedCallbackFunc)(int id, void *ptr);
typedef void (RTC_API *rtcErrorCallbackFunc)(int id, const char *error, void *ptr);
typedef void (RTC_API *rtcMessageCallbackFunc)(int id, const char *message, int size, void *ptr);

typedef struct {
    const char **iceServers;
    int iceServersCount;
    const char *proxyServer;
    const char *bindAddress;
    rtcCertificateType certificateType;
    rtcTransportPolicy iceTransportPolicy;
    bool enableIceTcp;
    bool enableIceUdpMux;
    bool disableAutoNegotiation;
    bool forceMediaTransport;
    uint16_t portRangeBegin;
    uint16_t portRangeEnd;
    int mtu;
    int maxMessageSize;
} rtcConfiguration;

typedef struct {
    bool unordered;
    bool unreliable;
    unsigned int maxPacketLifeTime;
    unsigned int maxRetransmits;
} rtcReliability;

typedef struct {
    rtcReliability reliability;
    const char *protocol;
    bool negotiated;
    bool manualStream;
    uint16_t stream;
} rtcDataChannelInit;

void rtcInitLogger(rtcLogLevel level, rtcLogCallbackFunc cb);
void rtcSetUserPointer(int id, void *ptr);
int rtcCreatePeerConnection(const rtcConfiguration *config);
int rtcClosePeerConnection(int pc);
int rtcDeletePeerConnection(int pc);
int rtcSetStateChangeCallback(int pc, rtcStateChangeCallbackFunc cb);
int rtcSetGatheringStateChangeCallback(int pc, rtcGatheringStateCallbackFunc cb);
int rtcSetLocalDescription(int pc, const char *type);
int rtcSetRemoteDescription(int pc, const char *sdp, const char *type);
int rtcGetLocalDescription(int pc, char *buffer, int size);
int rtcCreateDataChannelEx(int pc, const char *label, const rtcDataChannelInit *init);
int rtcSetOpenCallback(int id, rtcOpenCallbackFunc cb);
int rtcSetClosedCallback(int id, rtcClosedCallbackFunc cb);
int rtcSetErrorCallback(int id, rtcErrorCallbackFunc cb);
int rtcSetMessageCallback(int id, rtcMessageCallbackFunc cb);
int rtcSendMessage(int id, const char *data, int size);
int rtcClose(int id);
int rtcDeleteDataChannel(int dc);

#ifdef __cplusplus
}
#endif

#endif
