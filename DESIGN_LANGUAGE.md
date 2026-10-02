# NetSpeed design language

## Purpose and authority

NetSpeed is a network-measurement instrument. Its interfaces must make results
easy to understand, the measurement process visible, and the supporting evidence
available without overwhelming the summary.

This document is the canonical visual and information-architecture specification
for Standard, Observatory, and Phosphor. It consolidates the design brief into
one system; earlier layout sketches and theme-specific treatments are not
independent design authorities. It describes design requirements, not a claim
that every possible diagnostic field is already implemented.

Conflicts in the original brief are resolved in favor of its later sections.
Subsequent project decisions take precedence over that brief: Standard and
Observatory have distinct, deliberate material identities, not merely different
density on a visually neutral page. Phosphor is a full VT220-era terminal
presentation with selectable 80/132-column modes.

Measurement meanings and calculations remain governed by
[Measurement protocol v2](MEASUREMENT_PROTOCOL_V2.md) and
[HTTP measurement transport](HTTP_MEASUREMENT_TRANSPORT.md). Browser API routing,
credentials, CORS, and deployment remain governed by the
[HTTP deployment contract](HTTP_DEPLOYMENT.md). Browser orchestration and result
handling are implemented in the [measurement engine](web/js/speedtest.js) and
qualified by the [browser unit/contract tests](tests/web/) and
[browser smoke tests](tests/browser/). Presentation must not redefine those
contracts or invent evidence the engine does not provide.

The governing principle is:

> Summary answers “How is my connection?”
>
> Details answers “What exactly did you measure?”
>
> Evidence answers “Why should I believe it?”

## One product, three purposes

| Presentation | Design language | Primary purpose |
| --- | --- | --- |
| Standard | Premium carrier-grade infrastructure product | Fastest comprehension |
| Observatory | Dark precision measurement workstation | Evidence and diagnosis |
| Phosphor | VT220-era monochrome terminal, 80/132 columns | Dense technical inspection |

All three share the same measurement model, terminology, result availability,
warning semantics, action order, detail sections, and component vocabulary.
Standard and Observatory additionally share typography families, spacing units,
semantic colors, button and input geometry, and chart mathematics. All three
preserve accessibility. Material language, typographic emphasis, density,
plotting treatment, and evidence visibility may differ. Changing presentation
must never change a result or silently discard evidence. Shared styling does
not require visually identical presentations.

| Presentation | Material character |
| --- | --- |
| Standard | Soft, spacious, editorial; subtle depth, restrained illumination, large measurements, minimal chrome |
| Observatory | Precise, dense, instrumented; cooler surfaces, harder edges, micro-grid, recessed evidence regions |
| Phosphor | Monochrome, fixed-cell, terminal-native; character-grid geometry and period-computer restraint |

Remove arbitrary decoration, then add intentional visual identity through
composition, light, material, and typography. Limit each presentation to one or
two memorable ideas, not a catalog of effects:

- Standard: a coherent, softly illuminated result composition and a restrained
  connection ribbon.
- Observatory: a gridded measurement plane with a recessed inspector, and a
  compact live evidence timeline.
- Phosphor: a useful, coherent terminal throughout.

Everything else supports those signatures. None is a neutral documentation
page or a collection of unrelated component effects. Richer interactions and
texture are subordinate refinements, not additional headline components.

## Shared information hierarchy

### Layer 1: Results

Use one results header, not five independently colored metric cards.

1. Download and upload are the dominant measurements.
2. Unloaded latency is the third headline measurement, with less emphasis than
   throughput. Jitter and packet loss are secondary readings.
3. Server/node, city, network/provider, ASN, protocol, and measurement time form
   a subdued context line. Identify the client network separately from the
   server location when those are different facts.
4. Keep the primary action and test status together in one action bar.
5. Place throughput figures, latency under load, and packet delivery beneath
   the summary, separated mainly by whitespace and thin rules.
6. Show a compact measurement-notes strip only when there are limitations,
   warnings, or failures worth explaining.

The intended reading order is:

```text
Download          Upload          Unloaded latency
486.7 Mbps        92.4 Mbps        12.6 ms
                                  2.1 ms jitter · 0.20% packet loss

Redmond, Oregon · High Desert Fiber · Measurement time
Run again                         Test complete
--------------------------------------------------------------
Throughput figures: download / upload
Latency distributions: unloaded / during download / during upload
Packet delivery and selected path
Details & evidence
```

Numbers in this document are illustrative. Live interfaces must use real result
fields, correct units, and the engine's statistical definitions.

### Layer 2: Details and evidence

Provide a full-width workspace below the results, reachable through a clear
`View details` control. Standard starts with it collapsed. Observatory exposes
evidence more readily and adds a persistent inspector on wide screens. Phosphor
provides the same sections through terminal-style navigation.

Use six shared sections: Overview, Throughput, Latency, Packet delivery,
Transport & verification, and Raw evidence. Do not create a new card or
independent accordion for every additional diagnostic field.

### Component vocabulary

| Component | Responsibility |
| --- | --- |
| `app-shell` | Shared page frame, content width, navigation, and footer |
| `result-header` | Headline measurements, context, and overall state |
| `metric` | A labeled value, quiet unit, availability, and optional qualification |
| `action-bar` | Primary action, supporting actions, and current progress |
| `measurement-figure` | A technical plot with a title, units, summary, and evidence entry point |
| `stage-rail` | Compact sequence, real outcomes, and recorded timings |
| `evidence-table` | Aligned diagnostic labels and exact values |
| `details-section` | A shared detail section or purposeful disclosure |
| `evidence-inspector` | Observatory's selected measurement or stage evidence |
| `measurement-notes` | Conditional limitations, warnings, and failure explanations |
| `connection-ribbon` | Standard's compact schematic of observed endpoints, transports, and separately identified packet topology |
| `measurement-strip` | Exact evidence for Observatory's selected or inspected chart window |
| `evidence-stream` | Observatory's recent recorded events, with full history in Details |

Variants may recompose these components into columns or character-grid rows,
but must preserve their semantic order and responsibilities.

## Visual system

### Layout and spacing

Standard and Observatory use a centered shell with a maximum width of 1240 px.
Use an 8 px spacing grid: 8, 16, 24, 32, 40, 48, and 64 px. Align headings,
readings, controls, plots, and evidence columns to common edges.

Standard uses the more spacious intervals. Observatory uses tighter intervals
and may use 4 px subdivisions within aligned technical rows, without inventing
a separate spacing scale.

Use whitespace first, a divider second, and a containing surface only when it
clarifies grouping or interaction. Avoid a container around a card around a
chart around a footer. Most sections should sit directly on the page background.

Phosphor uses character cells and line heights instead of rounded web-card
geometry; its 80/132-column rules are defined below.

### Surfaces and borders

Standard and Observatory share surface roles, not compulsory identical hex
values. Map those roles to each presentation's material language. The following
is the baseline palette; changes must preserve the small, ordered hierarchy:

| Token | Standard dark | Observatory dark | Light | Use |
| --- | --- | --- | --- | --- |
| Page | `#111317` | `#090e15` | `#ffffff` | Charcoal canvas / cooler blue-black canvas |
| Surface 1 | `#181c22` | `#0e1620` | `#f6f8fa` | Quiet grouping or neutral controls |
| Surface 2 | `#202630` | `#111c29` | `#eef2f6` | Result material or evidence grouping |
| Surface 3 | `#283240` | `#172536` | `#e8edf3` | Exceptional elevation or active context |

These are available levels, not a requirement to use all four on every screen.
Elevation or recession comes from the relationship between a region and its
surroundings, not its token name alone. Do not introduce arbitrary extra navy or
gray surfaces for individual metrics. In light themes, preserve Standard's soft
layering and Observatory's precise, recessed inspection character rather than
inverting dark lighting into bright halos.

Use one-pixel neutral rules. Dark normal borders are
`rgba(255,255,255,.10)` and strong dividers are `rgba(255,255,255,.16)`.
Light equivalents are `#dce2e8` and `#ccd4dd`. Reserve strong dividers for major
structural boundaries; do not color borders by measurement category.
Section dividers may be slightly inset to follow the content's optical edges
rather than running edge-to-edge everywhere.

### Color and effects

Use one cool-blue product accent family for Standard and Observatory. The shared
palette uses `#2363ce` on light surfaces and `#80adf6` on dark surfaces. Related
blue-to-cyan trace treatments belong to this family, not new category palettes.
Observatory may use a restrained ice-cyan focus accent for active evidence and
measurement selection; it is not a new per-metric color or a success indicator.
Green, amber, and red communicate success, warning, and failure, respectively;
their shades must remain readable against the selected surface.

Color carries state, not the identity of every metric. Do not assign blue to
download, purple to upload, green to latency, orange to jitter, and pink to loss.
Differentiate chart series with labels, line styles, or restrained related hues.
State must also be expressed in text or symbols, never by color alone.

Flat fills are the default. Standard's primary Run control is the sole button
exception: deep blue with a subtle top-to-bottom tonal shift. No purple, shiny
gloss, rainbow borders, glowing cards, fluorescent fill bars, or large colored
shadows. Other controls remain flat.

Subtraction does not mean visual neutrality. Each presentation must have a
deliberate material identity. Controlled gradients, lighting, translucency,
texture, and depth are permitted where they establish hierarchy or reinforce
the presentation's character. Effects must be systemic and repeatable rather
than component-specific decoration.

Use this approved effects vocabulary; it is a budget, not a checklist requiring
every effect in every region:

| Presentation | Treatment | Placement and purpose |
| --- | --- | --- |
| Standard | Soft radial blue/cyan lighting | A roughly 600–800 px bloom behind the main readings on wide screens, darker at the edges; no obvious gradient panel |
| Standard | Subtle elevation and soft shadow | One result/action surface; the primary Run control uses the same material family, not a separate glossy treatment |
| Standard | Restrained trace illumination and translucent fill | Primary throughput figures; related blue/cyan trace, roughly 4–7% area opacity and at most a tiny 1–2 px bloom |
| Observatory | Faint measurement grid | Primary measurement/plot region; a 32 or 40 px micro-grid at roughly 2–3% opacity, with data axes separately labeled |
| Observatory | Recessed surface and subtle inset shadow | Persistent evidence inspector; distinguish inspection from the measurement plane |
| Observatory | Faint cyan selection wash and 2 px edge | Selected measurement/evidence target; retain a non-color selection indicator and update the inspector title |
| Observatory | Small active-node illumination | The running timeline stage only; completed nodes dim without acquiring halos |
| Phosphor | Barely perceptible text bloom; optional faint scanlines | Terminal text/surface only; no gradients, elevated surfaces, or graphical chart glow |

Standard has three effect families: hero lighting, result/action elevation, and
chart illumination/fill. The Run control's tonal shift belongs to result/action
material, not a fourth special treatment. Optional near-invisible grain at
roughly 1–2% opacity may soften the canvas; omit it if it competes with data or
adds unnecessary rendering cost. It is not a separate visual signature.

Observatory's stronger identity comes from geometry, density, and bounded
instrument treatments, not brighter effects on everything. A related
blue/blue-to-cyan accent gradient is permitted for primary emphasis in the named
regions, not as a different gradient on each component. Do not stack independent
glows, borders, and gradients on the same region. Outside the named regions,
keep surfaces and evidence flat. Opacity and size ranges are starting points,
not reasons to sacrifice contrast or force desktop effects onto narrow screens.

Define effects through presentation-scoped tokens and shared components. They
must work in idle, running, completed, unavailable, failed, light/dark, and
reduced-motion states. Lighting is not a success indicator; contrast, labels,
and focus must remain readable with effects removed.

Standard uses 8 px radii for necessary containing surfaces. Observatory's
measurement and inspection regions may use square or 2 px edges. Both share
6 px button and 5 px small-control radii. Do not add capsule or pill shapes as
decoration. Phosphor has square geometry and no container shadows.

### Typography

Standard and Observatory use one sans family and one monospace family for
technical evidence. Use tabular numerals for measurements and aligned numeric
columns. Maintain three emphasis roles: primary readings, ordinary labels/body
text, and secondary/technical evidence.

| Function | Size / line height | Weight |
| --- | --- | --- |
| Page title | 24 / 32 px | Semibold |
| Headline number | 38 / 44 px baseline; up to 48 px on wide screens | Medium |
| Section title | 15 / 22 px | Semibold |
| Body and controls | 14 / 21 px | Regular |
| Secondary text | 12 / 18 px | Regular |
| Technical monospace | 12 / 18 px | Regular |

Standard's headline numbers should be large, tightly but legibly kerned, and
tabular, using `font-variant-numeric: tabular-nums`. The decimal fraction may be
75–80% of the integer's size, with a small, quiet unit on the same baseline.
Treat the value as one number: preserve its complete accessible text, copying,
rounding, and exported precision. Styling a fraction must not change a result.
Use consistent optical spacing around `Mbps`, `ms`, and `%`; align values on
stable tabular baselines so decimals do not jump as readings update.

Use careful layering of the three emphasis roles rather than many competing
weights and micro-labels. Technical panels use tighter line height; explanatory
text gets more breathing room.
Standard and Observatory use ordinary sentence case. Avoid tracked uppercase
labels throughout the interface. Compact technical abbreviations and Phosphor's
terminal labels are legitimate exceptions.

Observatory uses more monospace text, rigidly aligned timestamps/counters, and
tighter vertical rhythm. Its compact readout may use 24–32 px numbers instead of
Standard's large hero scale, but it must not introduce another typography
system. Phosphor uses one fixed-cell face consistently across readings,
controls, tables, and plots.

### Controls and navigation

Standard and Observatory use one coherent control system:

- Primary: solid accent and a high-contrast label by default. Standard's Run
  control may use the restrained deep-blue tonal shift defined above.
- Secondary: neutral fill, one-pixel neutral border, the same control rhythm.
- Icon: consistent size, radius, and SVG stroke width; an accessible name and a
  tooltip when useful. Prefer a text label for actions whose meaning is unclear.
- Disclosure: a title and one consistent chevron or a clear `View details`
  action, not a decorative `DETAILS` strip beneath every measurement.

Hover changes luminance by only a few percent, not the control's entire hue.
Standard's Run control becomes slightly brighter, never purple or glossy.

Keep Run/Run again as the primary action. Pause or other supported run controls,
Share result, Export JSON, and View details remain secondary and have consistent
placement across presentations. Disable unavailable actions with an explanation.
Phosphor expresses the same action hierarchy as terminal commands and
function-key labels, not filled web buttons or graphical icon controls.

Place GitHub in the footer or an About/overflow area, not beside the primary
product controls. Presentation switching must retain a supported shared result.

### Charts and statistical labels

Charts are technical figures, not dashboard ornaments. Use a neutral background,
faint grid, sparse axes, a clear trace, direct units, and restrained annotations.
The data must be visually stronger than its chrome. Standard may use a thin,
luminous blue-to-cyan primary trace and a very restrained translucent fill.
Observatory uses more explicit, precise axes and tick marks, with a faint
measurement grid where useful. Keep secondary traces quieter and avoid nested
decorated plot containers. Chart math, units, and statistical meanings remain
shared even when rendering materials differ.

Use tiny monospace ticks, one clear baseline, sparse horizontal grid lines, and
direct contextual labels or trace-end labels where they fit. Prefer these to a
legend box. Align axes, labels, and figure summaries to a common vertical rhythm;
avoid label collisions rather than shrinking text beyond readability.

Keep download and upload figure proportions related. Present unloaded,
download-loaded, and upload-loaded latency together on a comparable scale, or
explicitly label scale differences. Missing data is an explained empty state,
not an invented flat line.

Label sustained/window throughput, peak rates, per-request rates, ranges, median,
p90, jitter, and variability according to what was actually computed. Aggregate
throughput is bytes across the relevant workers divided by the measured window,
not an individual worker's percentile renamed as link capacity. If a detail
distribution and the headline use different filtering or statistics, say so.
Do not recompute a different headline merely to fit a presentation.

## Presentation-specific composition

### Standard: premium infrastructure product

Standard is the default and the fastest path to understanding the connection.
Its character is soft, spacious, broad, and precisely aligned: a modern network
appliance with the typography and proportion of a premium product interface.
Deep charcoal and near-white themes use the same hierarchy and spacing.

Standard should possess a recognizable visual signature: subtle depth,
restrained blue illumination, premium typography, and carefully layered
surfaces. It must not reduce to flat rectangles on a dark background. It should
look expensive because the composition and material are coherent, not because
effects compete for attention.

Use the horizontal results header, dominant download/upload readings, quieter
latency/jitter/loss, one action/progress region, and technical figures below.
Put context in a subdued line rather than separate network-identity cards.
Keep the six-section evidence workspace collapsed initially. It must remain
obvious how to reach exact samples and verification details.

Compose the top roughly 35–40% of a typical desktop view as one designed region,
not a rigid viewport-height requirement. Download and upload each receive about
30% of the horizontal composition and latency about 20%; remaining space is
gutters and breathing room. Jitter and loss sit compactly beneath latency,
not in equal-weight fourth and fifth boxes. Reflow deliberately on small screens.

Treat the entire result region as one material, not five cards. Soft lighting
behind the headline numbers and a subtle shadow beneath the result/action
surface establish depth without drawing new boxes around each reading. One
signature 2 px horizontal measurement/status line ties that region together:
mostly neutral, with a blue progress segment and a small moving leading edge
only during a run. Completion settles quietly into a static line with a labeled
outcome. Do not label a partial or failed run successful merely because
measurement has stopped.

For completed results, add a quiet monospace measurement-signature row using
only supported observations, for example:

```text
WS reused · H2 · random/chunked · 4 flows · 18.4 s
```

Qualify fields that differ by direction/window instead of implying one universal
configuration. Omit unavailable fields; do not populate them from defaults or
infer total run duration by adding overlapping measurement stages.

The connection ribbon is a restrained schematic under the hero, with compact
endpoint/transport labels and supported latency/loss annotations. It can replace
separate connection-detail cards. Distinguish HTTP throughput, WebSocket latency,
and WebRTC packet delivery: they are not necessarily one serial path. A label
sequence such as `Client — WS/H2 — Node — TURN relay` must not imply that HTTP or
WebSocket traffic traversed TURN. Show separate branches or explicitly label
packet topology, and identify advertised versus observed facts. Collapse missing
branches honestly; an unverified path is not a reason to draw a plausible one.

Keep the figures and evidence below quieter. Primary throughput traces use the
restrained illumination/fill vocabulary; details do not acquire their own halos
or gradients. The rich Run control, precise axes, and optional grain support the
composition rather than becoming additional focal points. The page should be
recognizable as a premium network instrument, not a documentation site or a
collection of colored boxes.

### Observatory: dark precision laboratory

Observatory is a serious measurement workstation: denser, cooler, more technical,
and visually more assertive than Standard. Think contemporary lab instrumentation
or packet-analyzer software, not a neon science-fiction dashboard.

Observatory should possess a recognizable instrument-panel signature: cooler
surfaces, precise grid geometry, recessed evidence regions, dense tabular
information, and restrained active-state illumination.

It shares Standard's accent family, semantic colors, spacing units, type
families, controls, and data hierarchy. It does not have to share identical
surface values, region radii, or material treatment. Distinction comes from both
evidence visibility and the structured, instrumented composition.

On wide screens, use a two-pane measurement area:

```text
Summary / primary measurements / actions
--------------------------------------------------------------
Charts and current measurement     | Measurement inspector
                                  | Transport and provenance
                                  | Samples and counters
                                  | Verification and limitations
--------------------------------------------------------------
Sequence / recorded timings / measurement notes
Full details and raw evidence
```

Selecting Download, Upload, Loaded latency, Packet delivery, or a stage updates
the inspector in place. Do not spawn another decorative card. Retain a clear
route from the inspector to the full detail section and raw evidence.

Use harder-edged measurement regions, tighter spacing, precise axes/ticks, and
faint grid lines confined to the primary measurement region. Keep the inspector
slightly darker and recessed, like a built-in instrumentation bay, with one
inset border and restrained shadow. Suitable dark-theme cues are
`inset 0 1px 0 rgba(255,255,255,.03)` and
`inset 1px 0 0 rgba(255,255,255,.02)`, not a large outer shadow.

Selection can use a 2 px ice-cyan rule and faint background wash; the inspector
title updates with the selected Download, Upload, or Latency context. Its rows
use muted monospace labels and brighter monospace values in rigid columns.
Nearly imperceptible alternating row shading is permissible, not mandatory.
Align numeric columns, timestamps, and counters rigidly. Only display timestamps
and counters backed by actual observations; the material language must never
create fake instrumentation.

Where samples support inspection, charts may expose a fine vertical crosshair
and tiny value readout snapped to the nearest actual sample. Avoid a large
floating tooltip card. A measurement strip beneath the selected chart exposes
the exact window, not another result summary, for example:

```text
window 2/3 · 486.7 Mbps · 91.3 MB · 4 flows · 12 req · 1500 ms
```

Hover or selection updates the strip from that window's recorded fields; missing
fields stay absent or explicitly unavailable. Do not derive request counts,
bytes, or overlap from the headline. Provide keyboard/touch selection and a
persistent selected state, so evidence is not available only on hover.

Keep the seven-stage rail compact, with simple markers, names, outcomes, and
recorded timings. Its shared stage vocabulary is:

| Stage | Meaning |
| --- | --- |
| 01 Handshake | Metadata, capabilities, and server discovery |
| 02 Idle latency | Unloaded latency measurement |
| 03 Download | Download measurement |
| 04 Upload | Upload measurement |
| 05 Load response | Latency observed during directional load |
| 06 Packet path | Packet delivery measurement and selected topology |
| 07 Analysis | Result analysis and confidence evidence |

This is a progress presentation, not a claim that loaded probes occur only
after throughput stages. Timings and overlap must reflect actual engine events.
Use checkmarks or explicit success text only for successful outcomes; show
unavailable and failed states explicitly. Completing analysis must not turn
earlier failures into successes.

The rail should read as an event timeline and instrument state machine, not
seven cards. Use a horizontal or vertical hairline with small circular nodes,
aligned timings, labels, and observed values. Completed nodes dim; the active
node brightens and may have the only subtle node glow. Failed nodes use semantic
red plus a failure label. Historical outcomes remain readable. Inspection
selection is distinct from live stage state: viewing a completed measurement
must not make its timeline node appear to be running.

The compact live evidence strip at the bottom of the measurement region shows
only three or four recent recorded events, with full history in Details. For
example, when those events and fields are actually recorded:

```text
20:41:32.104  WS echo accepted · reused connection · RTT 12.6 ms
20:41:33.552  DL window 2 complete · 91.3 MB · 486.7 Mbps
20:41:34.007  Loaded probe accepted · overlap 100%
```

Use recorded event times and their actual precision. Identify elapsed times as
elapsed when wall-clock timestamps are unavailable. Never invent an event log
from the final result, reconstruct timestamps from stage durations, or imply
millisecond precision not captured by the engine. Preserve recorded history in
the result/raw evidence; shared results without history say so. Keep this stream
bounded visually and avoid auto-scrolling the page or announcing every event to
assistive technology.

Remove introductory design-philosophy copy from the page. The evidence itself
should explain the instrument. No oversized glowing nodes, rainbow enclosures,
or a separate futuristic graphic identity. The micro-grid/recessed inspector and
evidence timeline are its two memorable ideas; crosshairs, selection edges, and
window strips support them. Its visual richness comes from a coherent laboratory
material language, not a different neon theme for every measurement.

### Phosphor: full VT220-era terminal

Phosphor is a full monochrome terminal presentation, not a modern dashboard
with a terminal font. Its visual reference is VT220-era terminal typography and
interaction, matching the Glass TTY VT220 font. It is not an IBM XT or Apple II
reproduction, nor a literal terminal-protocol or operating-system emulator.

The entire surface follows the character grid: navigation, result rows, status,
commands, plots, detail tables, inspection sections, and footer. Browser-native
semantics and accessibility remain underneath; modern web-dashboard chrome does
not appear on top.

- Offer 80 and 132 columns, default to 80, and remember the user's choice.
- Budget the central data area in character cells, including rules and column
  gutters. Center it within the browser viewport instead of stretching rows.
- Use 132 columns for more side-by-side evidence, not larger decoration.
- Use one fixed-cell monospace face and consistent line rhythm, including the
  headline results. Establish emphasis through position, alignment, rules, and
  restrained intensity rather than oversized numbers or proportional type.
- No rounded corners, floating cards, pills, gradients, graphical icon buttons,
  or container shadows. Controls must read as text commands or menu entries.
- Use character-cell traces, distributions, counters, and tables rather than
  modern SVG/canvas chart widgets. Progress and stage state are textual; any
  progress indicator must obey the same character grid.
- On narrow screens, reflow sections and reduce plot width rather than forcing
  horizontal page scrolling or shrinking text to an unreadable size.
- Allow detail pages and vertical scrolling. A period-sized viewport is not a
  reason to truncate the measurement dataset.

Use a near-black background, soft phosphor-green primary text, dimmer green
secondary text, very dim green dividers, and a brighter green active state.
Amber and red are reserved for real warnings and errors. No green gradients or
bright green filled meters. Text bloom, if used, must be barely perceptible;
scanlines are optional and must never be more noticeable than the data.

Use the locally served [Glass TTY VT220 font](web/fonts/README.md), with its
20-pixel rendering size as the baseline and a fixed-cell monospace fallback.
The supplied
[nmedia reference](https://www.nmedia.net/chris/) informs typography and tone;
NetSpeed's own column, component, and measurement rules remain authoritative.

An 80-column composition can take this form:

```text
NETSPEED / LINK ANALYZER                                                20:41:32
--------------------------------------------------------------------------------
SERVER   REDMOND, OR                 NETWORK  HIGH DESERT FIBER / AS64512
PROTO    V2 / WS                     HTTP     H2
PATH     TURN RELAY                  STATUS   COMPLETE

             DOWN          UP          RTT        JITTER       LOSS
RESULT      486.7         92.4         12.6          2.1         0.20
UNIT         MBPS         MBPS          MS           MS            %

LOADED RTT   DOWN 27.9 MS / UP 34.8 MS
--------------------------------------------------------------------------------
F1 DETAILS   F2 THROUGHPUT   F3 LATENCY   F4 PACKET   ESC BACK
```

Use thin character-grid plots and aligned evidence tables. Detail section
selection is a terminal menu or command line, not a row of modern pill tabs.
Provide clickable
equivalents of F1 Details, F2 Throughput, F3 Latency, F4 Packet, and Escape/Back.
A `SHOW` section selector may provide terse console-style inspection of all six
detail sections. Keyboard flavor must not require keyboard-only operation.

Product-native labels such as `NETSPEED / LINK ANALYZER`, `READY`, and function
keys are appropriate. Do not claim `APPLE ][`, `64K RAM`, `PRODOS`, a system
disk, or fake boot/loading operations. The terminal identity comes from its
constraints and utility, not simulated boot screens.

## Details and evidence contract

### Shared sections

Expose available fields in the following sections. Availability depends on the
engine, provider, and transport; a field listed here is not permission to infer
or fabricate its value.

| Section | Evidence to preserve and expose |
| --- | --- |
| Overview | Exact test time and duration; server/node; client IP, location, ASN, and network; distance; provider and protocol/version; confidence and the reasons for any quality interpretation |
| Throughput | Sustained and peak rates; each measurement window; bytes transferred; request and worker/concurrency counts; variability; sample distributions; payload; fixed/chunked framing; application chunk size; flush behavior; HTTP protocol; content-length and framing verification |
| Latency | Unloaded, download-loaded, and upload-loaded distributions; min/median/p90/max; jitter; accepted samples; discarded samples and reasons; WebSocket/HTTP transport; connection reuse; warmups; cold samples; timer-resolution limitations; loaded-overlap evidence |
| Packet delivery | Transaction, forward, and reverse-ack loss; packets sent/received/lost; burst pattern and longest burst; WebRTC/DataChannel state; TURN/STUN path; relay/direct topology; ICE RTT and selected transport |
| Transport & verification | Exact measurement endpoints; advertised capabilities; negotiated selection; anti-compression checks; Cache-Control, Content-Encoding, and proxy-buffer suppression; accepted upload bytes; server ingestion duration; fallback reasons |
| Raw evidence | Full JSON result, including samples, receipts, rejected attempts, errors, capabilities, and fields without a purpose-built visualization; copy and download controls |

Long sample tables belong here, not in the headline layout. New telemetry must
be retained in the result and raw evidence before a dedicated visualization is
available. Do not throw information away because it lacks a polished component.

### Results versus supporting evidence

Present the answer first and a quiet evidence line beneath it. For example:

```text
Latency during download
27.9 ms median · +15.3 ms versus unloaded
5/5 measured probes accepted · WebSocket · persistent connection · 1 warmup
```

The evidence line opens the Latency section or updates Observatory's inspector.
Show the statistic, transport, subprotocol, reuse evidence, warmup count,
accepted/discarded counts, timing limitations, load overlap, and fallback reason.
Keep every raw sample available even when it is excluded from the headline.

A throughput inspection should similarly expose rate, transferred bytes,
window duration, workers, sample/rejection counts, payload, framing, chunk size,
flush behavior, HTTP protocol, encoding, and verification status. Application
chunk size must not be presented as network packet size. Advertised capability,
selected configuration, and observed verification are different facts.

### Honest limitations and availability

Use precise measurement notes rather than generic praise such as “Excellent
connection!” Examples include:

- Browser timer resolution limited three latency samples.
- Packet delivery used a TURN relay rather than a direct path.
- WebSocket latency fell back to HTTP; include the actual reason in details.
- One measurement stage failed; partial evidence remains available.

Do not display bare `N/A` without an explanation. Prefer `Not measured` plus a
specific reason such as `TURN relay unavailable`, with a detail entry point.
Distinguish pending, running, succeeded, unavailable, and failed states. A
finished test may contain unavailable or failed stages; do not imply that every
measurement succeeded.

In Standard and Observatory, warnings use a thin left rule and quiet semantic
amber tint, not large yellow cards. Keep the explanation and evidence link more
prominent than the container; errors retain a distinct semantic label/treatment.

Zero packet loss is a valid measured value. Missing values are not zeroes.
Below-resolution RTTs are censored samples, not literal zero latency or a
fabricated 0.01 ms observation. Retain the original observation and its timing
qualification in evidence; follow the measurement contract for statistics.

Expose confidence only when supported by the engine's definition and evidence.
Show its contributing limitations and counts; do not invent a score or quality
grade to fill a visual slot.

### Raw, exported, shared, and partial results

The full result is the durable source of evidence. JSON export must preserve
unknown fields, original sample data, verification receipts, errors, and values
such as `0`, `false`, and `null` without treating them as absent.

Partial or failed runs retain their available evidence and stage outcomes.
Compact shared links may carry only a subset; disclose that limitation rather
than implying they contain full provenance. Never reconstruct missing evidence
from a headline value. Preserve the supported shared result when switching
presentations, and explain when a result cannot be shared.

Render network-provided labels and raw evidence as text, not executable markup.

### Maps and secondary context

If a map is retained, it stays subordinate to measurements: desaturated tiles,
small markers, quiet attribution and controls, and no bright mapping-library
chrome competing with the hero. Distinguish server and client locations and
their accuracy. A plotted geographic relationship is not proof of a network
route or exact physical distance; preserve the engine's qualifications.

## Responsive behavior and accessibility

- Preserve semantic headings, figure captions, table headers, units, and the
  same result/detail order in every presentation.
- On smaller screens, stack throughput figures and latency distributions.
  Observatory's inspector becomes an in-flow inspection region rather than
  squeezing the measurement area.
- Wrap context and action rows deliberately. Test at 320, 390, 768, and 1440 px,
  and verify 132-column Phosphor on a viewport that can accommodate it.
- Keep long values and raw JSON contained within their detail region. They
  must not create horizontal scrolling of the entire page.
- Provide readable secondary text, visible keyboard focus, and non-color state
  indicators in light and dark themes. Phosphor may remain intentionally dark.
- Use proper tab/tabpanel semantics and keyboard navigation. Function-key
  shortcuts must have labeled clickable equivalents and must not interfere
  with ordinary text entry.
- Honor reduced-motion preferences. Animation may indicate live progress, not
  decorate completed measurements or delay access to evidence.

### Live motion and settling

Number interpolation is permitted only while a measurement is actively running.
It must not invent additional samples or enter exported data; final readings
settle immediately to the engine's values. Traces animate only as live data
arrives, never as a page-load or shared-result reveal. The progress leading edge
stops when the run ends, with a quiet transition rather than flashes, confetti,
or artificial delays. Reduced-motion mode uses immediate values and static
indicators. Update assistive status at meaningful milestones, not every visual
interpolation frame.

## Implementation and review discipline

Maintain one semantic layout and shared component vocabulary, not independently
styled HTML applications. The presentation entry points are
[`web/index.html`](web/index.html), [`web/alternate.html`](web/alternate.html),
and [`web/phosphor.html`](web/phosphor.html). Shared layout, evidence rendering,
and tokens live in [`web/js/layout.js`](web/js/layout.js),
[`web/js/evidence.js`](web/js/evidence.js), and
[`web/css/styles.css`](web/css/styles.css), respectively.

Make a subtraction pass before adding polish:

1. Remove decorative gradients.
2. Remove arbitrary glows.
3. Collapse the palette to product accent and semantic state.
4. Collapse unnecessary surface variations.
5. Normalize radii.
6. Normalize borders.
7. Normalize typography.
8. Remove unnecessary containers.
9. Arrange the shared hierarchy and variant-specific inspection layout.
10. Establish each presentation's deliberate material signature using the
    approved effects vocabulary; keep the remaining evidence quiet.

Every new border, background, shadow, gradient, glow, pill, color, or uppercase
treatment must have an information-hierarchy, interaction, or presentation-
character justification and a repeatable place in the system. “It looks themed”
is not a justification. The subtraction pass removes arbitrary decoration; it
does not ban controlled effects or end with visual neutrality. Tests must not
blanket-ban gradients or shadows that this specification permits.

Review changes against these acceptance criteria:

- The same result has identical meanings and availability in all three views.
- Download/upload dominate; latency is clear; jitter/loss/context are quieter.
- Standard and Observatory visibly share control geometry, type families,
  spacing logic, semantic colors, and measurement hierarchy, but are immediately
  distinguishable by composition and material rather than their page labels.
- Standard has a coherent premium result surface, restrained hero lighting,
  asymmetric metric composition, precise numerals, a signature status line, and
  an honest connection ribbon; it is not just flat rectangles on a dark canvas.
- Observatory has a precise measurement/evidence plane, recessed inspector,
  aligned technical data, a bounded recorded-event timeline, and restrained
  live-state illumination. Its density and visual richness serve useful
  provenance, not decorative instrumentation.
- Effects stay within the named presentation regions and reuse a small token
  vocabulary; Standard's Run control is the only tonal button exception and
  ordinary evidence remains flat. Each page has one or two memorable ideas, not
  fifteen. No per-metric rainbow identity, unrelated gradients, or accumulating
  panel halos return.
- Connection branches, event history, window strips, and measurement-signature
  fields come from evidence, not plausible topology or reconstructed telemetry.
- Phosphor is terminal throughout, with coherent character-grid geometry,
  textual controls, and character plots in both column modes.
- Detail sections expose accepted and discarded samples, overlap, transport,
  verification, and unavailable reasons when the engine supplies them.
- Full raw/exported evidence survives new telemetry, failures, and partial runs.
- Pending, running, successful, unavailable, failed, and shared-result states
  have been checked; finishing analysis does not erase earlier outcomes.
- Screenshots and keyboard checks cover desktop/mobile, themes, long values,
  empty states, and timer-limited measurements.
- The data is more prominent than the chrome. Each presentation has an
  intentional identity, while results, states, and focus remain understandable
  without effects and under reduced-motion preferences.
