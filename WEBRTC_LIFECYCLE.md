# WebRTC session lifecycle

This document is the canonical lifecycle contract for the packet-test server.
The packet frame and measurement formulas are defined by
[`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md).

The lifecycle includes bounded offer admission, client-bound report completion,
session ownership, concurrency, cancellation, and teardown. Authentication,
trusted client identity, rates, and TURN exposure are defined by
[`SERVICE_HARDENING.md`](SERVICE_HARDENING.md).

## 1. ownership invariants

The WebRTC manager is the only owner of the active-session map and the only
component allowed to remove a session from it.

Every close path follows the same order:

1. identify the exact session pointer associated with the test ID;
2. remove that session while holding the manager lock;
3. release the manager lock;
4. claim resource teardown through the session's `sync.Once`;
5. cancel the session context and stop its disconnect timer;
6. close the data channel and peer connection;
7. publish the terminal `closed` state and close the session's `Done` channel.

No Pion resource is closed while the manager lock is held. A close callback may
therefore re-enter the manager without deadlocking. Concurrent close requests,
cleanup, failure callbacks, report completion, and shutdown may all race, but
the data channel and peer connection each receive at most one owner close.

Mutable lifecycle fields are private and protected by the session mutex. Packet
counters use a separate read/write mutex so reports can take immutable snapshots
without racing message callbacks or blocking lifecycle transitions.

## 2. states

A session moves through these explicit states:

| state | meaning |
|---|---|
| `new` | allocated and registered, before SDP negotiation begins |
| `negotiating` | remote offer and local answer are being processed |
| `connecting` | Pion reports connection establishment in progress |
| `connected` | the peer, ICE layer, open data channel, or valid data proves connectivity |
| `disconnected` | a transient disconnect is inside its recovery grace period |
| `closing` | a close owner has been claimed; new work is rejected |
| `closed` | data channel and peer connection close calls have completed |

`closing` and `closed` are terminal. Late callbacks cannot move a terminal
session back into an active state.

## 3. offer admission and cancellation

`HandleOfferForClient` accepts the HTTP request context and the resolved client
identity. Before allocating a peer connection, the manager reserves one pending
offer against both the global and per-client session ceilings. Active sessions
and pending offers are considered together so concurrent signaling cannot race
past a limit. The reservation is released on every return path.

Admission is closed atomically with shutdown:

- the manager checks `closed` and increments the in-flight-offer wait group while
  holding the same lock;
- shutdown sets `closed` under that lock before waiting, so `WaitGroup.Add` cannot
  race with `Wait`;
- the ICE-server configuration is deep-copied for each new peer connection;
- the session is inserted into the map before any asynchronous Pion callback is
  installed;
- request cancellation, manager cancellation, peer failure, and ICE-gathering
  timeout all unwind the registered session through the common close path;
- manager and request cancellation are rechecked around asynchronous failure
  paths so the returned cause is not scheduler-dependent;
- a successful answer is returned only while the manager still owns the exact
  session and shutdown has not closed admission.

Deferred cleanup keys off the immutable `session.ID`, not a named return value.
This matters because an explicit error return overwrites named return variables
before deferred functions run.

The default ICE-gathering timeout is 10 seconds. The default maximum session
lifetime is 120 seconds.

## 4. disconnect recovery

A Pion `disconnected` state does not immediately destroy a session. The default
recovery grace period is five seconds.

Only the first disconnect transition schedules a timer. The session assigns the
timer a generation number. Any of these events proves recovery and invalidates
that generation:

- peer connection state becomes `connected`;
- ICE state becomes `connected` or `completed`;
- the packet-loss data channel opens;
- a valid packet-loss frame arrives.

Stopping a timer alone is not considered sufficient because its callback may
already be queued. The callback must also match the current generation and find
the session still in `disconnected` state before it can claim closure.

`failed` and `closed` states bypass the grace period and close immediately.

## 5. data-channel ownership

The daemon accepts one channel named `packet-loss` per session.

- unexpected channel labels are closed immediately;
- duplicate or late `packet-loss` channels are closed immediately;
- the accepted channel is detached from the session when its close callback
  runs;
- messages arriving after session cancellation or terminal state are ignored;
- valid frames update activity and cancel a stale disconnect grace period;
- packet counters are changed only through synchronized methods.

The exact 1,200-byte frame validation and acknowledgement accounting follow
the current measurement protocol contract.

## 6. idle cleanup and shutdown

The cleanup loop owns a cancellable manager context and is joined during
shutdown. For each scan it copies the session pointers under a read lock, then
claims expiration under each session lock. Expired sessions are removed and
closed outside the manager lock.

Shutdown is concurrent-idempotent:

1. close admission and cancel manager/session contexts;
2. wait for in-flight offer handlers to finish installing or unwinding callbacks;
3. wait for the cleanup loop to exit;
4. detach all remaining established sessions under the manager lock;
5. close those sessions outside the lock;
6. wait for every registered session teardown, including a session that a
   failure, report, disconnect timer, or cleanup pass removed immediately before
   shutdown took its map snapshot.

Session registration increments this drain barrier under the same manager lock
that closes admission. A session decrements it only after its data channel and
peer connection close calls complete and its `Done` channel is closed. This
prevents shutdown from returning while an already-detached Pion close is still
in flight.

After shutdown, ICE configuration updates are ignored and new offers return
`ErrManagerClosed`.

A completed packet report uses `CompletePacketLossSession(testID, clientKey)`.
The manager checks the creating client identity while holding its map lock,
removes the exact session, snapshots counters, and closes resources outside the
lock. Missing and wrong-owner IDs both return the same false result.

## 7. deterministic qualification

The Pion surface is wrapped behind small peer-connection and data-channel
interfaces. Tests use adversarial fakes to verify:

- 128 concurrent close callers still close each resource once;
- resource close callbacks can re-enter manager reads without deadlock;
- immediate failure callbacks cannot leave a later-inserted dead session;
- a delayed callback from an old session cannot remove a newer session that
  happens to reuse the same ID;
- canceled offers are removed even when explicit return values overwrite named
  result variables;
- ICE-gathering timeout removes and closes the partially negotiated session;
- queued disconnect callbacks cannot close a recovered session;
- valid data cancels disconnect grace;
- cleanup and shutdown close outside the manager lock;
- shutdown waits for a detached close already in progress;
- shutdown cancels and joins in-progress negotiation;
- concurrent shutdown is idempotent;
- ICE-server slices are deep-copied;
- lifecycle, statistics, configuration, and close operations are race-free;
- pending offers cannot exceed global or per-client capacity;
- packet reports cannot complete a session created by another client identity.

The external Pion/TURN interoperability test is part of release
qualification when the real modules and a relay environment are available.
