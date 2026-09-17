# Execution-alarm publication latency contract — 2026-09-01

## Hypothesis

- **Name:** immediate time-uniform execution-alarm publication.
- **Primary type:** maker/IOC execution.
- **Mechanism:** the executable-BBO drawdown e-process is valid under optional
  stopping.  Once its time-uniform boundary is crossed, waiting for the next
  five-minute horizon-selection bucket adds execution latency but no new
  evidence.  Publishing only the alarm transition immediately may therefore
  improve inventory action timing without shortening the 10/15/30-minute
  statistical scales.
- **Null:** immediate publication has no positive incremental executable value
  versus the current five-minute publication clock after identical IOC fees.
- **Existing baseline:** publish the already-detected alarm at the first BBO in
  the next UTC-aligned `horizonUpdateInterval` bucket.
- **Single integration point:** freshness of the existing downside/upside
  alarm snapshot supplied to an execution decision.  Direction, target,
  quantity, threshold, and order type remain unchanged.

## Causal clock

- **Prediction time:** the first observed executable BBO in a UTC minute.  The
  current minute and all later BBOs are unavailable to that prediction.
- **Primary horizon:** 10 minutes; predeclared sensitivity: 15 minutes.
- **Label maturity:** the first observed BBO at or after the alarm timestamp
  plus the selected horizon.
- **Update order:** observe first BBO of minute, emit a transition at most once,
  execute no earlier than the next BBO, then mature the terminal label.
- **Gap/reset rule:** a missing consecutive minute resets the e-process; the
  publication adapter cannot carry an event across that reset.
- **Overlap rule:** one signal per alarm episode; effective samples additionally
  de-cluster signals by the outcome horizon.  Six-hour chronological blocks
  are used for stability.

## Executable outcome

For a downside alarm, one unit of existing inventory is sold at the next
executable bid.  Its value versus holding to the terminal executable bid is

\[
V_{sell}=10^4\log(B_{exec}/B_{terminal})-f_{taker}.
\]

For the symmetric upside alarm, quote is converted at the next executable ask
and marked at the terminal executable bid:

\[
V_{buy}=10^4\log(B_{terminal}/A_{exec})-f_{taker}.
\]

Candidate and fixed-clock baseline use the same alarm, side, quantity and
taker fee.  Incremental value is `candidate value - baseline value`; fees
cancel only when both arms execute.  If the alarm recovers before the fixed
clock publishes it, the baseline is no action with zero value.

## Promotion boundary

Stages 0–2 may add only a removable publication adapter, focused tests and a
standalone scorer.  Strategy wiring, live YAML, binaries and services remain
unchanged.  Promotion requires a positive multiplicity-adjusted lower bound,
adequate effective samples, and chronological block stability.  Component
replay must then verify next-BBO execution, fees, inventory limits and range
turnover before any strategy regression or live proposal.

## Stage 2 result

The final scorer used all complete available ETHJPY archive days from
`2026-07-24T00:00:00Z` through `2026-09-01T00:00:00Z`, with six hours of causal preload, the production
10/15/30-minute e-process mixture, 1.282 confidence setting, 10 bps taker fee,
and a five-minute fixed-clock baseline. It retained the first BBO of each
minute exactly as the production sequential clock does; an action executed no
earlier than the next observed BBO.

Fourteen alarm episodes were eligible (`N_eff=13`). Immediate publication was
about 115.65 seconds earlier on average but had `-2.74245 bps` incremental
value versus fixed-clock publication. BUY was `+3.39585 bps`; SELL was
`-8.88076 bps`. Six of thirteen six-hour signal blocks were positive. The
two-horizon simultaneous lower bound was `-11.27196 bps`. The 15-minute sensitivity has the same paired execution-price
difference because both arms acted before terminal marking.

The gate returned `INCONCLUSIVE_SAMPLE`: even the complete local archive has
an effective sample count below 24, while the observed aggregate and SELL
point estimates are negative. The event-only production
adapter was removed after screening. The standalone scorer and manifest remain
to make the rejection reproducible:

- `cmd/gammacapture-mm-research/execution_alarm_latency_study.go`
- `data/gammacapture/research/ETHJPY-execution-alarm-latency-gate-2026-09-01.json`

No strategy, live YAML, binary, or service was changed.
