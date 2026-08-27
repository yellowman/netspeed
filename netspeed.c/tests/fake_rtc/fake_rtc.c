#include <rtc/rtc.h>

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#define TEST_PC 1
#define TEST_DC 2
#define TEST_FRAME_SIZE 1200
#define TEST_PACKET_TYPE_ACK 2

static void *pc_user;
static void *dc_user;
static rtcStateChangeCallbackFunc state_callback;
static rtcGatheringStateCallbackFunc gathering_callback;
static rtcOpenCallbackFunc open_callback;
static rtcClosedCallbackFunc closed_callback;
static rtcErrorCallbackFunc error_callback;
static rtcMessageCallbackFunc message_callback;

static void write_u64(uint8_t *destination, uint64_t value)
{
    for (int index = 7; index >= 0; index--) {
        destination[index] = (uint8_t)value;
        value >>= 8;
    }
}

void rtcInitLogger(rtcLogLevel level, rtcLogCallbackFunc cb)
{
    (void)level;
    (void)cb;
}

void rtcSetUserPointer(int id, void *ptr)
{
    if (id == TEST_PC) pc_user = ptr;
    if (id == TEST_DC) dc_user = ptr;
}

int rtcCreatePeerConnection(const rtcConfiguration *config)
{
    (void)config;
    return TEST_PC;
}

int rtcClosePeerConnection(int pc)
{
    if (pc == TEST_PC && state_callback) state_callback(pc, RTC_CLOSED, pc_user);
    return 0;
}

int rtcDeletePeerConnection(int pc)
{
    (void)pc;
    return 0;
}

int rtcSetStateChangeCallback(int pc, rtcStateChangeCallbackFunc cb)
{
    (void)pc;
    state_callback = cb;
    return 0;
}

int rtcSetGatheringStateChangeCallback(int pc, rtcGatheringStateCallbackFunc cb)
{
    (void)pc;
    gathering_callback = cb;
    return 0;
}

int rtcSetLocalDescription(int pc, const char *type)
{
    (void)type;
    if (gathering_callback) gathering_callback(pc, RTC_GATHERING_COMPLETE, pc_user);
    return 0;
}

int rtcGetLocalDescription(int pc, char *buffer, int size)
{
    (void)pc;
    static const char offer[] = "v=0\r\na=fake-netspeed-offer\r\n";
    int needed = (int)sizeof(offer);
    if (!buffer) return needed;
    if (size < needed) return -4;
    memcpy(buffer, offer, sizeof(offer));
    return needed;
}

int rtcSetRemoteDescription(int pc, const char *sdp, const char *type)
{
    (void)sdp;
    (void)type;
    if (state_callback) state_callback(pc, RTC_CONNECTED, pc_user);
    if (open_callback) open_callback(TEST_DC, dc_user);
    return 0;
}

int rtcCreateDataChannelEx(int pc, const char *label, const rtcDataChannelInit *init)
{
    (void)pc;
    (void)label;
    (void)init;
    return TEST_DC;
}

int rtcSetOpenCallback(int id, rtcOpenCallbackFunc cb)
{
    (void)id;
    open_callback = cb;
    return 0;
}

int rtcSetClosedCallback(int id, rtcClosedCallbackFunc cb)
{
    (void)id;
    closed_callback = cb;
    return 0;
}

int rtcSetErrorCallback(int id, rtcErrorCallbackFunc cb)
{
    (void)id;
    error_callback = cb;
    return 0;
}

int rtcSetMessageCallback(int id, rtcMessageCallbackFunc cb)
{
    (void)id;
    message_callback = cb;
    return 0;
}

int rtcSendMessage(int id, const char *data, int size)
{
    if (id != TEST_DC || !data || size != TEST_FRAME_SIZE || !message_callback) return -1;
    uint8_t *ack = malloc(TEST_FRAME_SIZE);
    if (!ack) {
        if (error_callback) error_callback(id, "allocation failure", dc_user);
        return -1;
    }
    memcpy(ack, data, TEST_FRAME_SIZE);
    ack[5] = TEST_PACKET_TYPE_ACK;
    write_u64(ack + 20, 1U);
    message_callback(id, (const char *)ack, TEST_FRAME_SIZE, dc_user);
    free(ack);
    return 0;
}

int rtcClose(int id)
{
    if (id == TEST_DC && closed_callback) closed_callback(id, dc_user);
    return 0;
}

int rtcDeleteDataChannel(int dc)
{
    (void)dc;
    return 0;
}
