# Long-running stability and adaptive transport validation

This investigation starts from v0.4.1 (`bd405b6`) and targets three reported
symptoms: a tunnel that starts but cannot deliver traffic until restarted,
failures after extended use, and automatic FEC reducing useful throughput on
some paths. Improvements keep the default automatic policy; no new tuning
parameters are required.

## Reproduced implementation failures

| Trigger | Failure | Change and verification |
| --- | --- | --- |
| Many different recoverable loss patterns on one session | Reed–Solomon inversion matrices accumulate for the lifetime of the cached codec | Bound large-profile inversion caching; 20,000 different 32+8 erasures retain about 0.43 MB after the change, versus 53.67 MB before it. Every reconstructed source shard is checked. |
| Fragment table is full of incomplete packets | Even the final fragment of an existing packet is refused; every arrival scans the whole table | Permit existing assemblies to finish, use heap-based expiry, and clear idle or retired-session state. With 2,046 incomplete packets, the microbenchmark falls from 115–169 microseconds to 1.13–1.26 microseconds per assembly. |
| A session's send worker exits while receive remains alive | The session still appears usable, but outgoing packets only fill queues | Retire the entire failed session and reconnect. A real QUIC regression checks delivery, an induced send failure, automatic redial, and delivery again. |
| FEC groups arrive out of order | A later group prematurely marks earlier source frames lost | Honor the existing completion grace period; regressions cover reordered data and late close packets. |
| Repeated connection loss events | Bounded history still copies all retained events on every insertion | Use a ring buffer while preserving chronological snapshots. |
| Listener setup or send-queue shutdown fails | Owned sockets or queued buffers survive the failed operation | Close the socket on listen failure; serialize queue closure with enqueue and release pending owned buffers. |
| DNS changes or peer edits overlap | Retired route ownership or an unrelated peer's current endpoint can be lost | Retain failed lease releases for retry, bound candidate backoff history, and preserve live state for peers outside the edit transaction. |
| DNS/handshake work stalls | Status waits for the refresh transaction lock | Publish an immutable status snapshot independently of slow refresh work. |
| Windows reports startup while an address is Tentative | Traffic cannot leave until duplicate-address detection finishes | Disable DAD on the owned Wintun IP interfaces before assigning addresses, following WireGuard Windows' point-to-point interface practice. |
| A lower bandwidth follows an earlier peak | The nominal rolling maximum retains the old peak forever | Recompute the maximum from samples still inside the window. Regressions cover peak retention, expiry, subsequent increases, and application-limited traffic. |
| A configured interleave profile reaches its first data frame | The automatic controller silently resets its interleave setting | Preserve the configured minimum while allowing automatic burst protection to increase it. |
| Closed sessions are replaced after their bounded history is evicted | Replacement links survive forever without a corresponding history entry | Retain pending links only while the old session is still active; publish closure before removing it from the active map. The regression previously retained 936 orphan links after 1,000 closures. |
| Route removal repeatedly fails while DNS keeps changing | Preserving ownership alone still permits an unlimited retired-route backlog | Stop new automatic migrations when 64 retired or unsafe leases are retained; continue retrying safe cleanup, preserve the active route, and resume without candidate backoff after cleanup succeeds. |
| A peer-set preparation fails before every transition is built | Rollback dereferences an absent transition; a successful pre-commit rollback also leaves the live generation behind the core | Skip untouched transitions and publish restored generations before releasing reservations. Regressions cover partial preparation, failed readiness, unrelated migrations, and subsequent generation-checked updates. |
| A desktop mutation waits behind another long Windows operation | The caller's cancellation cannot interrupt the mutex wait | Use a cancellable serialized operation gate. Regressions check cancellation, exclusive ownership, and independent status access. |
| Windows needs the tunnel adapter identity | Starting NetAdapter/CIM discovery adds seconds to each network-policy operation | Resolve alias → LUID → index through native IP Helper and retain the LUID for scoped network changes. Native tests cover discovery and operation failures. |
| PowerShell network cleanup consumes the 13-second host shutdown budget | Service reports a failed shutdown even though the core exits normally | Delete owned addresses and routes through native IP Helper using the captured interface LUID. A real two-adapter test checks IPv4/IPv6 deletion, unrelated route preservation and repeated cleanup before destroying either adapter. |
| Windows configures addresses and routes at startup | PowerShell and CIM startup dominate a small amount of network work | Configure MTU/DAD and create temporary addresses and active routes through native IP Helper. Record ownership per successful operation for rollback; keep existing DNS apply semantics. Compare both address families against the original path on real Windows 10 and 11 adapters. |
| Windows still loads DNS cmdlets during startup | DNS application adds several seconds after native address/route setup | Apply only requested DNS fields and address families through the native API. Preserve ordered servers, suffix and search policy; retain cleanup ownership across partially successful calls. Verify real default-resolver queries through the tunnel. |
| Windows resets tunnel DNS during shutdown | DNS cmdlet initialization alone adds several seconds | Use native interface DNS settings on supported Windows, retaining the cmdlet fallback only when the API is absent. Compare effective DNS and registry state against the old operation on real adapters. |
| Many loss or ACK callbacks describe the same outstanding packet flight | The bandwidth model is repeatedly reduced before feedback can describe the first reduction | Share one sent-packet recovery boundary across queue, loss and ECN feedback. Regressions cover delayed old-flight feedback, mixed signals and explicit ECN during RTT recalibration. |

The inversion-cache result is retained Go heap after collection, not whole
process RSS. The fragment result is a targeted CPU benchmark, not a claim of
the same speedup for an entire tunnel.

## Controlled network measurement

Use `tests/benchmark/run.sh` with isolated container namespaces and real TUN
interfaces. Record source commit, binary hash, CPU limits and affinity, memory
limits, link model, FEC mode, and actual qdisc packet/drop counters for every
trial. Compare identical source snapshots; a Git command run inside an
unversioned source export may otherwise find the parent checkout's newer HEAD.

Disabling interface GSO alone is insufficient: QUIC can still submit
`UDP_SEGMENT` batches. A batch may count as one packet at the loss qdisc,
whereas protected FEC traffic uses ordinary writes. The first off/auto matrix
was discarded after measured loss differed between modes. Corrected loss and
policer trials also set `QUIC_GO_DISABLE_GSO=1` on both endpoints. This control
applies to the QUIC modes; it does not establish an equivalent direct
WireGuard comparison.

The four independent models are:

- A clean 50 Mbit/s path with 10 ms delay in each direction.
- Independent 2% packet loss in each direction at the same rate and delay.
- Burst loss averaging 2%, with approximately eight consecutive packets per
  burst, at the same rate and delay.
- A real 50 Mbit/s token-bucket policer with a 32 KiB burst allowance and
  10 ms one-way delay. This drops excess packets instead of building a queue.

Random loss, bursts and policing require different responses. A recovered
source frame is useful evidence for FEC, but redundancy consumes the same
policed wire budget as data. Low queueing delay alone does not prove spare
capacity. These are synthetic path models, not measurements of a particular
ISP's classification or filtering system.

## Adaptive-policy comparisons

Four corrected batches contain 64 trials. Each cell in the comparisons below
is the mean of two trials, run in reversed order. Ordinary trials last 30
seconds; the bandwidth-step trials last 45 seconds. These are small samples,
so the smaller differences should not be treated as precise speedup claims.

The isolated rolling-bandwidth-window change compares `a797059` against the
same source with only the change later committed as `f88e362`:

| Path | Before, Mbit/s | After, Mbit/s |
| --- | ---: | ---: |
| Clean | 42.565 | 42.516 |
| Independent loss | 31.264 | 30.617 |
| 50 Mbit/s policer | 22.441 | 23.558 |
| 50 → 10 → 50 Mbit/s | 30.835 | 30.779 |

On the policer, the two trials' total counted drops fell from 1,294 to 693,
and parity's share of wire datagrams fell from 12.79% to 8.30%. Restoring the
50 Mbit/s link still restored approximately 42.6 Mbit/s application delivery.
This fixes a demonstrably stale estimate; it is not a complete policer
detector or a promise to fill every ISP rate limit.

The isolated automatic interleaving change compares `b199e2f` with `487450a`:

| Path | Before, Mbit/s | After, Mbit/s |
| --- | ---: | ---: |
| Clean | 42.501 | 42.405 |
| Independent loss | 30.549 | 30.848 |
| Burst loss | 9.570 | 12.119 |
| 50 Mbit/s policer | 22.755 | 24.222 |

Burst throughput increased 26.6% in this pair of trials. The controller now
recognizes repeated heavy losses even in short groups. It also keeps burst
interleaving long enough to collect evidence in source frames and grows the
repair window with the extra lanes. Source frames still leave immediately.
Without that window adjustment, splitting a timer-limited group across more
lanes can create tiny groups and excessive parity. Random-loss residual
source losses fell from 68 to 10 in these two trials; clean-path parity stayed
around 0.06% of wire datagrams. The policer result varies substantially between
repeats, so the small mean gain is not conclusive.

More delivered traffic can produce more absolute losses on the same random
path. Compare losses against sent traffic as well as effective TCP delivery;
do not use raw recovered or dropped counts alone as the success criterion.
Application send-queue drops are backpressure and are distinct from network
losses. All 64 corrected trials maintained an established QUIC session and
reported zero QUIC receive-queue drops.

An additional deterministic erasure experiment runs the real encoder,
controller, and decoder across two immutable source revisions. It covers two
loss models, three packet rates, ten seeds, and 20,000 source frames per case.
Every reconstructed payload is checked. At 5,000 frames/s under burst loss,
residual source loss changed from 1.5325% to 1.1135%, with 1.44% more wire
traffic. Independent-loss outputs remained identical in this experiment.
This is evidence about packet recovery and overhead, not a simulated TCP
throughput result.

Several attractive-looking changes were rejected: scaling parity solely by
partial-group size substantially increased residual loss; increasing
interleaving without changing its timer doubled overhead in some short-group
cases; globally delaying parity reductions inflated low-rate overhead.
Congestion-sampling simulations also exposed regressions under short RTT and
batched acknowledgments. Those experimental policies were not shipped.

A further 12 diagnostic trials changed only the steady probing gain on
`487450a`. On the policer, gain 1.0 increased mean delivery from 24.48 to
29.34 Mbit/s across two repeats while counted drops fell from 1,038 to 395.
Gain 0.9 instead produced 971 drops: the bandwidth estimate reacts to the
change, so a smaller gain does not guarantee a lower actual pacing rate.
Single clean/random-loss trials showed no large regression at 1.0, but the
bandwidth-restoration phase delivered 41.57 versus 42.78 Mbit/s. This supports
investigating bounded adaptive probing; it does not justify globally reducing
the gain or asserting that loss with a short queue identifies ISP policing.
The diagnostic constants were not applied to production.

A bounded adaptive pacing prototype was also rejected after 12 additional
real-network comparisons. In a separate complete event capture, none of the
488 recorded events entered its pressure probe: individual ACK timing and
RTT variation kept postponing eligibility. Throughput differences therefore
cannot be credited to that policy. This negative result, the exact prototype
patch, and the test data are retained in the
[prototype review](benchmarks/2026-10-stability/auto-probe-review.json). Making the
threshold looser without fixing the sampling model would not establish safe
policer detection.

The compact [measurement archive](benchmarks/2026-10-stability/manifest.json)
contains per-trial counters, source and binary hashes, resource limits, and
fixture changes. It distinguishes the 64 formal trials from the 12 fixed-gain
diagnostics and excludes the earlier invalid GSO comparisons.

## Sustained baseline and initial fixes

Two 900-second trials used FEC auto plus Salamander, 50 Mbit/s in both
directions, 20 ms base RTT and 2% independent loss per direction. Each endpoint
had one CPU of quota, `GOMAXPROCS=1`, a 512 MiB container limit and a 192 MiB Go
soft memory budget. The initial fixed source was `2df5f5e`, before the later
adaptive-controller and Windows changes.

| Measurement | v0.4.1 | Initial fixes |
| --- | ---: | ---: |
| Application TCP goodput | 31.950 Mbit/s | 31.950 Mbit/s |
| Longest zero-write interval at the TCP sender | 0.750 s | 0.500 s |
| Receiver FEC-recovered source frames | 58,800 | 58,410 |
| Receiver residual source loss | 92 | 103 |
| Sender/receiver final RSS | 43,444 / 46,608 KiB | 42,012 / 47,576 KiB |
| Process exit or permanent disconnection | None | None |

Both versions sustained this particular workload. One run each does not
establish a meaningful throughput or RSS improvement. The deterministic
failure regressions are stronger evidence for the specific fixes than these
ordinary-loss soak averages. These stall values come from the sender's 250 ms
application-write intervals, not receiver delivery. Data already buffered in
the TCP socket can still arrive during a zero-write interval. Summing individual
zero intervals does not mean the connection was continuously disconnected for
that total duration. These 15-minute runs do not establish multi-day stability.

## Forty-minute changing-path run

An immutable `bba728e` build completed 2,400 seconds with automatic congestion
control, automatic FEC and Salamander. Each endpoint again had one CPU,
512 MiB of memory and a 192 MiB Go soft memory budget. Socket GSO and interface
offloads were disabled. The receiver delivered 37,814,796,288 bytes; both
process observation IDs and QUIC session IDs stayed unchanged.

| Five-minute phase | Receiver goodput, Mbit/s |
| --- | ---: |
| 50 Mbit/s, 20 ms RTT, independent 2% loss | 30.821 |
| Lossy Wi-Fi profile | 4.540 |
| Fiber profile | 385.083 |
| Cellular profile | 3.859 |
| DSL profile | 11.108 |
| Lossy Wi-Fi profile, repeated | 4.103 |
| Fiber profile, repeated | 380.841 |
| Cable profile | 187.999 |

This run exposed a performance problem in that source: the cellular phase included
seven consecutive one-second receiver intervals with no data. That profile
combines a 50/15 Mbit/s asymmetric link, 70 ms base RTT, 15 ms jitter in each
direction, 1% loss and 0.2% explicit reordering. Outer QUIC acknowledgments
continued while internal send queues filled and inner TCP RTT rose to almost
eight seconds. These observations locate the stall below application
delivery; they do not by themselves prove whether jitter classification,
queueing, reordering, or CPU scheduling caused it. It is not recorded as an
unqualified stability pass.

Final sender/receiver RSS was 89,796 / 62,256 KiB; process lifetime peak RSS
was 151,460 / 87,892 KiB. Resource sampling covered only the last approximately
15 minutes. File descriptors remained at 14 per core throughout those samples.
The receive queues reported 3 / 18 drops, unlike the zero receive-queue drops
in the 64 short formal trials. The receiver recovered 19,974 source frames
and recorded 669 residual losses. The compact
[run record](benchmarks/2026-10-stability/changing-path-40m.json) preserves
receiver gap intervals, phase results, provenance and raw-result hashes.

## Follow-up: repeated congestion feedback

The changing-path stall was reproducible with the same cellular profile.
Removing jitter restored approximately 31 Mbit/s; removing only explicit
reordering did not. Inspection then found a concrete amplifier: every lost
packet could multiply the model by 0.85 while a standing queue was detected,
and ACK queue responses had a separate reduction timer. A single outstanding
flight could therefore collapse the pacing model through repeated reports of
the same condition.

Commit `34bb3df` gives loss and ACK queue feedback a shared recovery boundary:
a reduction records the largest sent packet number, and older-flight feedback
cannot reduce the model again even if detection spans several RTTs. In two
300-second cellular comparisons, receiver goodput changed from 2.266 / 2.658
to 26.218 / 25.475 Mbit/s. The longest consecutive zero-delivery receiver
intervals changed from 9 / 14 seconds to zero whole one-second intervals.
Inner TCP maximum RTT changed from 9.20 / 9.58 seconds to 0.805 / 1.320 seconds.
The senders had no measured cgroup CPU throttling; receivers had zero except
0.416 ms accumulated in one fixed trial. These results
support the specific repeated-feedback fix; they do not establish that all
jitter classification or long-term mobile-network problems are solved.

A 24-trial boundary matrix also compared clean, independent-loss, short-RTT,
policer, cellular and 50 → 10 → 50 Mbit/s paths. Clean and short-RTT means were
essentially unchanged; the step profile recovered approximately 42.8 Mbit/s
in both versions. Its brief low-rate phase was lower after the change
(7.99 → 7.29 Mbit/s), so four additional static 10 Mbit/s trials checked that
boundary: mean delivery was 8.458 → 8.432 Mbit/s. Both versions still exhibited
occasional inner-TCP delay on that small pipe, including a two-second receiver
gap in one fixed trial. This change removes the observed cellular collapse;
it does not promise an absence of short stalls on every path.

The same review found repeated ECN reductions. Commit `0b79b97` uses the same
flight boundary for validated CE reports. Explicit congestion on a new flight
still responds immediately without requiring a queue threshold or waiting for
a possibly stale RTT timer. Tests exercise the actual ECN tracker as well as
mixed loss/CE ordering. Real ECT-to-CE marking was verified by packet capture;
clean and approximately 2% CE comparisons showed no substantial throughput
regression. This is a feedback-accounting correction, not a claim of an ECN
throughput breakthrough or a new ISP-classification algorithm. Source hashes,
per-trial data, CPU sampling limits and reproduction controls are retained in
the [cellular investigation](benchmarks/2026-10-stability/cellular-review.json)
and [ECN investigation](benchmarks/2026-10-stability/ecn-review.json).

## Windows and recovery fixtures

[`tests/windows/soak-lifecycle.ps1`](../tests/windows/soak-lifecycle.ps1)
records real peer reachability after each startup, continuing delivery, and
shutdown. The initial QEMU guest is Windows 10 Enterprise LTSC Evaluation,
build 19044.1288; its results must not be described as Windows 11 results.

The v0.4.1 baseline completed 20 cycles. Median first ping after startup was
2,973.5 ms (range 2,743–3,234 ms). Separate address-state sampling showed
Tentative-to-Preferred transitions taking 3.206–3.294 seconds. Native Windows
command-plan regressions fail before the DAD ordering change and pass after it.

With the DAD change, all 20 starts delivered their first ping on the first
attempt: median 2 ms, range 1–61 ms. Nineteen complete start/stop cycles passed;
one shutdown exceeded its network-cleanup budget while the guest had heavy
Windows servicing and Defender activity. The core itself exited normally.
This result demonstrates removal of the address-readiness delay, not a claim
that every lifecycle timeout has been eliminated.

A separate 12-before/12-after native-index comparison completed every cycle.
For the six immediate-stop rounds, median startup changed from 9.68 to 8.13
seconds and shutdown from 5.85 to 4.53 seconds. With a three-second hold before
shutdown, medians changed from 9.81 to 9.60 and 6.71 to 5.99 seconds. Adapter
discovery itself changed from approximately 2.4–2.7 seconds to 6–7 ms, but
NetTCPIP initialization and background guest work still affect total latency.
These measurements use Windows 10 and are not Windows 11 performance claims.

[`tests/network/soak-probe`](../tests/network/soak-probe/README.md)
checks every byte of bidirectional TCP traffic and records delivery gaps and
application reconnects. It does not restart the tunnel. Fault timestamps must
be recorded separately so an intentional outage can be distinguished from a
failure to recover after the path becomes usable again.

The first complete Windows traffic run used `c3175de` and an updated Windows
10 build 19044.7725. Over 600.002 seconds it checked 637,206,528 bytes in each
direction. The core and service process IDs stayed unchanged through 5-second
and 40-second outages, 5% loss phases, and a peer restart. The four application
TCP connections represent application recovery, not VPN restarts. Recovery
after the two outages was observed within 1.25 seconds of path restoration;
after the peer restart, it took up to 12.61 seconds. These bounds include the
one-second sampling granularity and measured host/guest clock uncertainty.
The longest delivery gap, 40.878 seconds, includes the deliberate 40-second
outage. The Linux peer in this test was v0.4.1.

This run did **not** pass full lifecycle acceptance: its final shutdown again
spent the entire 13-second host-cleanup budget inside PowerShell. The core
then exited in approximately 64 ms. The failed result and service-error
details are retained in [the Windows traffic record](benchmarks/2026-10-stability/windows10-traffic.json).
It motivated the subsequent native Windows cleanup work; a successful traffic
test must not conceal a shutdown failure.


With native address/route cleanup (`eafe3f7`), all ten minimal and all ten
DNS-policy cycles passed on Windows 10. Median shutdown was 452.5 ms without
DNS and 6,511.5 ms with DNS; median startup was still 10,116 / 11,601 ms.
Every first ping succeeded on its first attempt. These measurements isolate
why DNS cleanup and startup remained worth improving after route cleanup.
The [cycle record](benchmarks/2026-10-stability/windows10-native-lifecycle.json)
retains every round and the source/binary hashes.

A second complete 600-second traffic run on the same immutable source verified
652,673,024 bytes in each direction and ended with a successful shutdown.
The tunnel's process and observation IDs remained unchanged across the fault
schedule, while the application reconnected three times. The longest delivery
gap, 40.871 seconds, includes the intentional 40-second outage. Independent
DNS API regression queries ran on other test adapters as background load.
This run did not retain a host/guest clock-offset bracket, so its recovery
durations are reported as nominal cross-clock observations rather than exact
upper bounds. In particular, a peer restart still took approximately 15 seconds
to recover; the QUIC idle-detection bound has not been removed. See the
[successful traffic record](benchmarks/2026-10-stability/windows10-native-traffic.json).

Native DNS reset (`3612627`) subsequently passed all three real Windows 10
semantic comparisons: mixed IPv4/IPv6 plus suffix, suffix only, and IPv4 only.
The resulting effective DNS and registry values matched the original cmdlet,
including inherited defaults and preservation of an unrelated search-list
value and second adapter. Native resets took 1.49–2.38 ms, versus 5.26–6.04
seconds for the corresponding cmdlet operations; those timings exclude the
comparison queries. See the [DNS comparison](benchmarks/2026-10-stability/windows-dns-reset.json).

Five subsequent DNS-policy cycles on that immutable source all passed, with
median shutdown 340 ms (262–419 ms) and every first ping succeeding immediately.
Startup remained 9,284 ms median because the native startup change was not
present yet. The [cycle record](benchmarks/2026-10-stability/windows10-native-dns-lifecycle.json)
includes the cold first round and actual runtime executable hashes. These are
sequential virtual-machine observations, not a controlled claim of an identical
speedup on every Windows installation.

Native startup (`6d48e0a`) also passed the strengthened Windows 10 comparison:
IPv4 and IPv6 addresses were Preferred immediately after their native creation,
and all measured state matched the old PowerShell path. The measured address,
route and MTU/DAD operation took 64 ms versus 3.48 seconds for the reference;
these exclude total service startup and DNS. See the
[API comparison](benchmarks/2026-10-stability/windows10-native-startup.json).

Five subsequent Windows 10 DNS-policy cycles all passed on that immutable source.
Median total startup was 5,967 ms (5,698–7,636 ms), shutdown 446 ms (377–844 ms),
and every first ping succeeded on its first attempt. The existing DNS apply
cmdlet remains on the startup path, so native address/route timings must not
be presented as complete startup latency. The
[native-startup cycle record](benchmarks/2026-10-stability/windows10-native-startup-lifecycle.json)
includes all rounds and runtime hashes.

## Windows 11 desktop acceptance

A separate QEMU guest runs Windows 11 Enterprise LTSC Evaluation 24H2,
build 26100.1742, with Secure Boot and TPM 2.0 enabled. Tests use the installed
desktop broker layout, including the root manager executable, and verify that
its SHA-256 matches the runtime quick executable. A scheduled interactive task
runs the ordinary-user cases with a real filtered token: medium integrity,
administrator group deny-only, and no enabled administrator membership.
These exercise the desktop management path and user permissions; they are not
a claim of manual visual testing of every desktop screen.

The earlier native-cleanup source `eafe3f7` completed ten administrator
DNS-policy cycles and ten limited-token minimal-policy cycles. Every first
ping succeeded on its first attempt. Median startup/shutdown was
12.284 / 5.971 seconds for DNS policy and 14.445 / 0.472 seconds for the
limited-token minimal configuration. The cold first administrator startup
took 26.734 seconds and remains in the record. These sequential VM timings
include background guest activity and must not be treated as controlled
hardware-independent speedup ratios.

With `6d48e0a`, five further DNS-policy and five limited-token minimal-policy
cycles all passed, again with every first ping successful immediately.
Median startup/shutdown was 10.665 / 0.765 seconds with DNS, and
7.060 / 0.424 seconds for the limited-token minimal configuration. Together,
these two sources completed 30 of 30 desktop-broker cycles. The
[lifecycle record](benchmarks/2026-10-stability/windows11-lifecycle.json)
keeps every round, token evidence and executable identity. The remaining
DNS application cost in this source motivated a separate native DNS apply
comparison; these measurements do not include that later change.

The native-startup source `6d48e0a` completed a 600.005-second Windows 11
checked-traffic run with automatic FEC and Salamander. It verified
1,018,036,224 bytes in each direction with no payload mismatch, then completed
desktop-broker shutdown successfully in 796 ms. The core observation ID and
process ID stayed unchanged throughout, while QUIC and application connections
recovered automatically. The probe reports 31 socket errors separately from
payload validation, including two counted at the end of the test; that counter
must not be relabeled as corrupted data or silently discarded.

Host/guest clock brackets were captured around the fault phases. Recovery
upper bounds, including sampling and measured clock uncertainty, were
2.586 seconds after the five-second outage and 1.576 seconds after the
forty-second outage. Peer restart recovery took up to 13.948 seconds after
the peer was ready; this remains an idle-detection limitation. The longest
delivery gap was 40.912 seconds, including the intentional forty-second
outage. See the [Windows 11 traffic record](benchmarks/2026-10-stability/windows11-traffic.json)
for clock envelopes, process resources, source/binary hashes and final exit.
Across 120 resource samples, core working set changed from 71.43 to 54.31 MiB
with an 84 MiB peak; private bytes changed from 65.88 to 80.19 MiB. Core handles
changed from 236 to 249, while manager handles changed from 232 to 226.
A ten-minute sample does not establish the absence of multi-day resource growth.


## Native DNS application and final lifecycle checks

Commit `86254f0` removes the remaining DNS cmdlet from normal startup on
supported Windows. The native path preserves unspecified address families,
the listed server order within each family, a suffix when only servers are
provided, and both server lists when only a suffix is provided. It leaves the
separate search-list setting unchanged. Successfully applied steps remain
owned for cleanup if a later step fails or is canceled; only an unavailable
API takes the original cmdlet fallback.

Four real comparison cases passed on both Windows 10 and 11, starting from
nonempty IPv4/IPv6 DNS and suffix values on two live adapters, with search-list
preservation tested separately.
The first candidate incorrectly cleared an omitted address family; the
comparison caught it and production was corrected to match the existing
cmdlet behavior. Native and legacy snapshots now agree. Only trailing NUL
padding in registry NameServer strings is normalized, with raw snapshots
retained and internal NUL/nonzero suffix differences still rejected.

On Windows 11 the DNS operation took 1.16–1.27 ms versus 5.42–6.02 seconds for
the corresponding cmdlet; these are API timings, not complete startup times.
The final five DNS-policy cycles on immutable `86254f0` all passed:

| Guest | Startup median (range) | Shutdown median (range) |
| --- | ---: | ---: |
| Windows 10 | 4.676 s (4.590–4.930) | 450 ms (377–591) |
| Windows 11 desktop broker | 6.095 s (5.813–7.699) | 374 ms (337–823) |

Every first ping succeeded on its first attempt. Each round also resolved a
fresh test name through the default Windows resolver, without an explicit
server override. Every answer was `203.0.113.73`, with a matching query from
the Windows tunnel address recorded by the isolated peer. The
[DNS responder](../tests/network/dns-probe/README.md) and lifecycle fixture
make that check repeatable. Earlier one-round DNS baselines also passed on
both guests, confirming the original resolver path before switching binaries.

These are sequential VM samples, including all rounds; background work can
affect their timing, so they do not establish a universal percentage speedup.
The final DNS change affects host policy application. The 600-second Windows
11 fault run above used `6d48e0a` and was not rerun under the later DNS source;
its transport implementation is unchanged. The additional DNS acceptance
covers real network state, partial failure, complete lifecycle and actual
name resolution. Exact source/binary identities and observations are in the
[Windows 10 DNS record](benchmarks/2026-10-stability/windows10-dns-apply.json)
and [Windows 11 DNS record](benchmarks/2026-10-stability/windows11-dns-apply.json).

## Verification scope

Verification includes the root Go race suite, fork congestion and actual ECN
tracker regressions, Windows platform unit tests, native Windows API comparisons,
ARM64/386 cross-compilation and the repository's CI workflows. CI also exercises
the independent Rust protocol implementation, Linux two-peer behavior, FreeBSD,
and desktop/OPNsense/OpenWrt packaging. A pre-existing accept-queue test assumed
one particular refusal timing; it now accepts refusal during Dial or the first
stream operation while strictly checking the same ConnectionRefused code and
queue-slot recovery. It passed 100 ordinary and 40 race repetitions before CI.

The longest changing-path run is forty minutes. No result here establishes
multi-day uptime, every ISP traffic policy, ARM64 runtime behavior, or the
absence of short stalls on every low-rate path. The approximately fourteen-second
peer-restart recovery remains a measured limit. These boundaries and rejected
experiments are retained alongside the successful checks.

## References

- [WireGuard Windows interface setup](https://git.zx2c4.com/wireguard-windows/plain/tunnel/addressconfig.go)
  disables DAD when preparing its tunnel interface.
- [Linux BBR implementation](https://github.com/torvalds/linux/blob/master/net/ipv4/tcp_bbr.c)
  uses a recent windowed bandwidth maximum and treats long-term policer
  sampling as a separate mechanism.
- [RFC 9265](https://www.rfc-editor.org/rfc/rfc9265.html)
  discusses interactions between recovery and congestion control. It is
  background guidance, not the wg-quic protocol specification.
