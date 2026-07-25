You are a senior quantitative developer, Go engineer, market-microstructure researcher, and production trading-systems engineer.

Build a production-quality algorithmic spot-trading application by extending the open-source BBGO trading framework:

* Repository: `c9s/bbgo`
* Exchange: Binance Japan Spot
* Target instruments: dynamically discovered JPY-quoted spot markets
* Trading direction: long-only spot
* Primary implementation language: Go
* Notifications and runtime control: Telegram through BBGO’s notification and interaction systems
* Strategy ID: `gammacapture`
* Default operating mode: replay or paper trading
* Live trading must remain disabled until explicit acceptance gates pass

The strategy must combine:

1. Gamma Capture-inspired discrete barrier-crossing volatility measurement.
2. Directional upward and downward crossing-intensity estimation.
3. A Skellam terminal displacement model.
4. Finite-horizon TP-before-SL and SL-before-TP probabilities.
5. Doob-upcrossing-inspired signal hysteresis and churn detection.
6. Adaptive take-profit and loss-cut decisions.
7. BBGO’s exchange session, order execution, position accounting, persistence, metrics, notification, and interaction infrastructure.
8. Detailed runtime reports and urgent alerts delivered through Telegram.

Do not claim that Doob’s upcrossing lemma guarantees profitability. Do not claim that barrier-crossing counts are automatically Poisson. Treat the Poisson, Skellam, renewal, and independence assumptions as hypotheses that must be validated empirically.

# 1. BBGO-first development approach

Do not build a parallel standalone trading framework.

Implement the strategy as a BBGO strategy package following the current BBGO strategy lifecycle.

At the start of development:

1. Inspect the current `c9s/bbgo` source tree.
2. Select and pin a tested BBGO release tag or commit.
3. Record that tag or commit in:

   * `go.mod`;
   * Docker image labels;
   * startup logs;
   * Telegram startup reports;
   * backtest reports.
4. Verify all BBGO interfaces against the pinned version.
5. Do not depend on a floating `main` branch in production.

The strategy package should follow a structure similar to:

```text
pkg/strategy/gammacapture/
├── strategy.go
├── config.go
├── state.go
├── subscribe.go
├── model.go
├── crossing.go
├── first_passage.go
├── signal_state.go
├── execution.go
├── risk.go
├── telegram.go
├── metrics.go
├── report.go
├── persistence.go
├── validation.go
└── *_test.go
```

Register the strategy using the current BBGO registration mechanism:

```go
const ID = "gammacapture"

func init() {
    bbgo.RegisterStrategy(ID, &Strategy{})
}
```

Implement the appropriate BBGO lifecycle methods, including:

* `ID() string`;
* `InstanceID() string`;
* `Subscribe(session *bbgo.ExchangeSession)`;
* `Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error`.

Use BBGO types and services wherever they are suitable:

* `bbgo.ExchangeSession`;
* `bbgo.OrderExecutor`;
* `types.Position`;
* `types.Market`;
* `fixedpoint.Value`;
* market-data streams;
* user-data streams;
* BBGO persistence;
* BBGO shutdown hooks;
* BBGO notification and interaction packages;
* BBGO metrics infrastructure.

Do not duplicate BBGO’s balance, position, trade, or order accounting unless a clearly documented gap requires an isolated extension.

# 2. Binance Japan and JPY-market constraints

Implement long-only Binance Japan spot trading.

Never use:

* margin;
* borrowing;
* futures;
* perpetual swaps;
* options;
* leverage;
* synthetic short exposure.

Never submit a sell quantity larger than the available owned base-asset inventory.

The application must discover eligible JPY markets dynamically rather than relying solely on hard-coded symbols.

Eligible markets must satisfy:

* quote currency is `JPY`;
* market is available in the connected Binance session;
* spot trading is enabled;
* symbol is currently tradable;
* required order types are supported;
* symbol passes configured spread, depth, volume, and minimum-notional requirements.

Support two symbol-selection modes:

```yaml
symbolSelection:
  mode: dynamic
  quoteAsset: JPY
```

and:

```yaml
symbolSelection:
  mode: allowlist
  symbols:
    - BTCJPY
    - ETHJPY
```

An optional denylist must override dynamic discovery:

```yaml
symbolSelection:
  denylist:
    - EXAMPLEJPY
```

Because BBGO’s normal strategy subscription lifecycle may expect symbols before streams are connected, determine the safest implementation after inspecting the pinned BBGO version:

* a multi-symbol strategy instance;
* generated strategy instances before startup;
* a BBGO-compatible preflight configuration generator;
* or another lifecycle-compatible method.

Do not modify BBGO core merely to support dynamic discovery unless no safe extension mechanism exists.

Load market precision and limits through the BBGO session and exchange market metadata. Apply:

* tick size;
* quantity step size;
* minimum quantity;
* maximum quantity;
* minimum notional;
* maximum notional when applicable;
* price restrictions;
* order-count limits;
* supported order types.

Use `fixedpoint.Value` or another exact decimal representation already compatible with BBGO. Do not use binary floating point for order price, quantity, balances, fees, or P&L accounting.

# 3. Market-data subscriptions

Subscribe to the highest-quality market data supported by the pinned BBGO Binance adapter.

Prefer:

* individual market trades;
* aggregate trades if individual trades are unavailable;
* best bid and ask;
* order-book depth where reliable;
* user order, trade, and balance events.

Use BBGO’s market-data stream for public data and user-data stream for:

* order updates;
* fills;
* commissions;
* balance changes;
* reconnect state.

Filter every callback by symbol because BBGO session streams can be shared by several strategies.

Maintain:

* exchange event timestamp;
* local receive timestamp;
* sequence or update identifier when supplied;
* event source;
* symbol;
* stream generation after reconnect.

Detect:

* stale market data;
* out-of-order events;
* duplicated events;
* sequence gaps;
* reconnects;
* local clock drift;
* missing order updates;
* user-data-stream unavailability.

Never manufacture a price path through a market-data gap.

# 4. Reference price

Provide configurable reference-price modes:

```yaml
referencePrice:
  mode: microprice
```

Supported modes:

* midpoint;
* microprice;
* last trade;
* robust midpoint;
* last non-stale reference price.

Default to microprice when a synchronized order book is available:

[
P_t^{micro}
===========

\frac{
A_t Q_t^b+B_t Q_t^a
}{
Q_t^b+Q_t^a
},
]

where:

* (A_t) is best ask;
* (B_t) is best bid;
* (Q_t^a) is ask quantity;
* (Q_t^b) is bid quantity.

Fallback to midpoint if depth quantities are invalid.

Do not generate barrier crossings from a stale, crossed, locked, or unsynchronized book unless explicitly allowed in research mode.

# 5. Barrier-crossing engine

Define:

[
Y_t=\log P_t.
]

For barrier width (h>0), define log-price grid levels:

[
B_k=Y_{\mathrm{anchor}}+kh.
]

Maintain an integer grid state (J_t).

A completed upward crossing occurs when the reference price moves through the next upper barrier. A completed downward crossing occurs when it moves through the next lower barrier.

Requirements:

* crossings must be chronological;
* crossings must be non-overlapping;
* count every completed barrier in a multi-barrier movement;
* do not count repeated updates at the same level;
* do not use future data to revise previous crossings;
* distinguish ordinary, gap-affected, and uncertain crossings;
* support a minimum dwell time;
* support a reversal buffer;
* support a maximum number of crossings accepted from one discontinuous update;
* persist the anchor, barrier width, grid state, and crossing counters;
* restore them safely after restart.

Each crossing event must contain:

```go
type CrossingEvent struct {
    Symbol          string
    Direction       Direction
    FromState       int64
    ToState         int64
    BarrierWidth    float64
    ReferencePrice  fixedpoint.Value
    ExchangeTime    time.Time
    ReceiveTime     time.Time
    GapAffected     bool
    StreamGeneration uint64
}
```

Use the appropriate exact or stable numerical types in the actual implementation.

# 6. Barrier-width selection

Make barrier width configurable and stable within an epoch.

Possible rule:

[
h_t=
\max\left(
h_{\min},
c_s\frac{\text{spread}*t}{P_t},
c_v\widehat{\sigma}*{short,t}
\right).
]

Do not change (h) after every market event.

Use scheduled barrier epochs such as:

* fixed duration;
* session boundary;
* regime-change boundary;
* flat-position-only boundary.

Prefer changing barrier width only while flat.

Reject candidate widths that produce:

* excessive bid-ask bounce;
* insufficient crossing events;
* estimated edge smaller than execution cost;
* highly unstable intensity estimates;
* barrier distances smaller than practical tradable movement.

Persist an epoch identifier with every crossing.

# 7. Gamma Capture volatility

For interval (T):

[
N_T^+=\text{upward crossings},
]

[
N_T^-=\text{downward crossings},
]

[
N_T=N_T^++N_T^-.
]

Calculate:

[
\widehat{\sigma}_{GC}
=====================

h\sqrt{\frac{N_T}{T}}.
]

Provide:

* rolling estimate;
* exponentially weighted estimate;
* clean-crossing estimate;
* gap-adjusted estimate;
* confidence interval;
* observation count;
* effective sample duration;
* estimate age.

Compare it with:

* conventional realized volatility;
* Parkinson volatility where applicable;
* ATR-based movement;
* short-term return standard deviation.

Do not use the comparison directly as a trade signal until validated.

# 8. Directional intensities

Start with the research hypotheses:

[
N_T^+\sim\operatorname{Poisson}(\lambda^+T),
]

[
N_T^-\sim\operatorname{Poisson}(\lambda^-T).
]

Use Gamma priors:

[
\lambda^+\sim\operatorname{Gamma}(\alpha_+,\beta_+),
]

[
\lambda^-\sim\operatorname{Gamma}(\alpha_-,\beta_-).
]

Posterior means:

[
\widehat{\lambda}^+
===================

\frac{\alpha_++N_T^+}{\beta_++T},
]

[
\widehat{\lambda}^-
===================

\frac{\alpha_-+N_T^-}{\beta_-+T}.
]

Calculate:

[
p_t=
\frac{\widehat{\lambda}^+}
{\widehat{\lambda}^++\widehat{\lambda}^-},
]

and:

[
\nu_t=
\widehat{\lambda}^++\widehat{\lambda}^-.
]

Expose:

* upward intensity;
* downward intensity;
* total intensity;
* intensity ratio;
* directional probability;
* expected next-crossing time;
* posterior credible intervals;
* effective observation size;
* model-health status.

# 9. Poisson-model validation

Continuously or periodically test:

* crossing count mean versus variance;
* interarrival-time exponentiality;
* autocorrelation;
* overdispersion;
* underdispersion;
* directional dependence;
* duration dependence;
* intraday seasonality;
* conditional rate stability;
* cross-excitation;
* volatility clustering;
* structural breaks.

Define model-health levels:

```text
HEALTHY
DEGRADED
INVALID
INSUFFICIENT_DATA
```

Actions:

* `HEALTHY`: normal operation;
* `DEGRADED`: reduce size and increase uncertainty penalty;
* `INVALID`: block new entries;
* `INSUFFICIENT_DATA`: collect observations without trading.

Support alternative models behind interfaces:

```go
type CrossingModel interface {
    Update(CrossingEvent)
    Snapshot(time.Time) ModelSnapshot
    Validate() ValidationResult
}
```

Alternatives may include:

* empirical Markov transition model;
* renewal model;
* Hawkes model;
* Markov-modulated Poisson model.

# 10. Skellam model

For horizon (H):

[
N_H^+\sim\operatorname{Poisson}(\lambda^+H),
]

[
N_H^-\sim\operatorname{Poisson}(\lambda^-H),
]

[
X_H=N_H^+-N_H^-.
]

Then:

[
X_H\sim\operatorname{Skellam}(\lambda^+H,\lambda^-H).
]

Map displacement to price using:

[
P_{t+H}=P_t\exp(hX_H).
]

Calculate:

* PMF;
* CDF;
* mean displacement;
* variance;
* skewness;
* excess kurtosis;
* terminal profit probability;
* terminal loss probability;
* expected marked value;
* tail probabilities;
* cost-adjusted expected value.

Implement numerically stable calculations and test them against a trusted independent implementation.

Do not describe Skellam tails as power-law heavy tails.

# 11. Finite-horizon first-passage engine

Do not use only terminal Skellam probabilities for TP/SL.

Model barrier state (j) as a continuous-time birth-death process:

[
j\rightarrow j+1
\quad\text{at rate }\lambda^+,
]

[
j\rightarrow j-1
\quad\text{at rate }\lambda^-.
]

Set:

* take-profit boundary: (+G);
* stop-loss boundary: (-L);
* current state: (j\in(-L,G)).

For remaining horizon (H_r), calculate:

[
q_t=
\Pr(
\tau_{+G}<\tau_{-L},
\tau_{+G}\le H_r
\mid\mathcal F_t
),
]

[
s_t=
\Pr(
\tau_{-L}<\tau_{+G},
\tau_{-L}\le H_r
\mid\mathcal F_t
),
]

and:

[
u_t=1-q_t-s_t.
]

Use:

* generator matrix exponentiation;
* uniformization;
* verified dynamic programming;
* or another stable finite-horizon method.

Verify against Monte Carlo simulations.

Use infinite-horizon gambler’s-ruin formulas only as diagnostics and unit-test references.

# 12. Expected continuation value

Calculate:

[
EV_{\mathrm{hold}}
==================

## q_t\Pi_{TP}

s_t\Lambda_{SL}
+
u_t EV_{\mathrm{unresolved}}
----------------------------

## C_{\mathrm{fees}}

## C_{\mathrm{spread}}

## C_{\mathrm{slippage}}

## C_{\mathrm{impact}}

## R_{\mathrm{gap}}

R_{\mathrm{model}}.
]

Every entry and continuation decision must use net expected value after:

* fees;
* spread;
* slippage;
* impact;
* uncertainty;
* gap risk;
* stale-model penalty.

# 13. Doob-inspired signal state machine

Apply upcrossing logic to a bounded confidence signal, not directly as a profitability theorem.

For example:

[
Z_t=q_t
]

or a calibrated net-edge score mapped to ([0,1]).

Choose:

[
0<a<b<1.
]

A completed signal upcrossing requires:

1. (Z_t\le a);
2. later (Z_t\ge b);
3. count it once;
4. do not count another until the signal resets to (a) or below.

Define downcrossings analogously.

Maintain strategy states:

```text
INITIALIZING
WARMING_UP
DISARMED
ARMED_LONG
ENTRY_PENDING
LONG
WEAKENING
EXIT_PENDING
COOLDOWN
SUSPENDED
HALTED
RECOVERING
```

Define churn:

[
C_t=U_t(a,b)+D_t(a,b).
]

Use churn to detect unstable signals:

[
C_t\ge C_{\max}
\quad\text{and}\quad
M_t<A_{\min}
\Longrightarrow
\text{exit and cooldown}.
]

Do not use crossing count alone as directional alpha.

# 14. Entry logic

Enter long only when all conditions hold:

* strategy status is running;
* symbol is enabled;
* warm-up is complete;
* model health is healthy or explicitly permitted degraded;
* completed confidence upcrossing has occurred;
* (q_t\ge q_{\mathrm{entry}});
* (EV_{\mathrm{hold}}\ge EV_{\min});
* expected edge exceeds all costs;
* spread is acceptable;
* order book is synchronized;
* market data is fresh;
* user-data stream is healthy;
* JPY balance is sufficient;
* symbol exposure is below limits;
* portfolio exposure is below limits;
* cooldown is inactive;
* daily risk limits have not been reached.

Do not enter merely because:

[
\lambda^+>\lambda^-.
]

# 15. Position sizing

Size from the minimum of:

* stop-risk budget;
* available JPY budget;
* per-symbol notional cap;
* portfolio exposure cap;
* liquidity cap;
* market-impact cap;
* model-confidence cap.

Example:

[
Q_{\mathrm{risk}}
=================

\frac{R_{\mathrm{trade}}}
{P_{\mathrm{entry}}-P_{\mathrm{hardSL}}}.
]

Round using BBGO market metadata.

Use conservative fractional edge scaling. Do not use unrestricted Kelly sizing.

# 16. Take-profit mechanism

Implement layered profit management.

## Initial TP

Choose (G) only when expected reward after costs satisfies the configured reward-to-risk and expected-value requirements.

## Profit activation

When:

[
J_t\ge A,
]

switch to trailing-profit mode.

## Event-time trailing stop

Track:

[
M_t=\max_{u\le t}J_u.
]

Exit when:

[
J_t\le M_t-R_t.
]

Make (R_t) wider when:

* TP-first probability remains high;
* upward intensity remains persistent;
* signal churn remains low.

Make (R_t) tighter when:

* probability declines;
* downward intensity increases;
* churn rises;
* spread widens;
* holding horizon approaches expiry;
* model health deteriorates.

## Marginal-value TP

Take profit when extending the target has non-positive marginal expected value.

## Partial TP

Support optional partial exits, but validate:

* remaining position exceeds minimum quantity;
* remaining notional is tradable;
* additional fees are justified;
* protective orders are resized correctly.

# 17. Loss-cut mechanism

Implement independent loss controls.

## Hard stop

Always maintain a non-negotiable hard stop:

[
J_t\le-L_{\mathrm{hard}}.
]

It must override model confidence.

## Expected-value stop

Exit when:

[
EV_{\mathrm{hold}}\le EV_{\mathrm{exit}}.
]

## Probability stop

Exit when:

[
q_t\le q_{\mathrm{exit}}.
]

## Signal downcrossing

Exit or reduce after a completed continuation-signal downcrossing.

## Adverse-intensity shock

Exit when posterior evidence indicates a materially adverse intensity regime.

## Churn stop

Exit when excessive confidence oscillation occurs without sufficient favorable movement.

## Time stop

Exit when:

* remaining horizon is too short;
* TP-first probability is too low;
* expected next-crossing time exceeds the remaining horizon;
* maximum holding time is reached.

## Operational stop

Block entries and safely exit or protect positions during:

* stale data;
* order-book desynchronization;
* user-stream failure;
* balance mismatch;
* unresolved execution state;
* database failure;
* excessive API errors;
* severe time drift;
* manual emergency stop.

# 18. BBGO position and control interfaces

Implement the BBGO-compatible interfaces appropriate to the pinned version.

At minimum, support:

```go
type PositionReader interface {
    CurrentPosition() *types.Position
}
```

```go
type PositionCloser interface {
    ClosePosition(
        ctx context.Context,
        percentage fixedpoint.Value,
    ) error
}
```

```go
type StrategyStatusReader interface {
    GetStatus() types.StrategyStatus
}
```

```go
type StrategyToggler interface {
    StrategyStatusReader
    Suspend() error
    Resume() error
}
```

```go
type EmergencyStopper interface {
    EmergencyStop() error
}
```

Where compatible, embed or use BBGO’s `StrategyController`.

Required behavior:

* `Suspend`: block new entries but preserve position protection;
* `Resume`: resume only after health checks;
* `EmergencyStop`: block new entries, cancel strategy orders, flatten or safely protect positions according to configured emergency policy;
* all methods must be idempotent;
* control actions must be persisted;
* control actions must generate Telegram reports.

# 19. Telegram configuration

Use BBGO’s Telegram notifier and interaction infrastructure.

Use environment variables:

```shell
TELEGRAM_BOT_TOKEN=
TELEGRAM_BOT_AUTH_TOKEN=
```

Never store bot tokens in:

* YAML committed to Git;
* source code;
* logs;
* Telegram reports;
* database records.

Require private authenticated Telegram chats.

Add an optional chat allowlist:

```yaml
telegram:
  enabled: true
  allowedChatIDs:
    - 123456789
```

Reject commands from unauthorized users or chats.

Do not allow Telegram availability to determine trading-loop availability. A Telegram outage must not block:

* market-data handling;
* risk checks;
* protective exits;
* order reconciliation.

# 20. Telegram runtime reports

Implement a `RuntimeReporter` that produces immutable snapshots.

```go
type RuntimeReporter interface {
    Snapshot(ctx context.Context) RuntimeSnapshot
    RenderTelegram(RuntimeSnapshot) []string
}
```

A runtime snapshot must include:

## Application

* bot name;
* strategy ID;
* strategy instance ID;
* BBGO version or commit;
* application git commit;
* environment;
* live, paper, replay, or backtest mode;
* startup time;
* uptime;
* hostname or instance identifier;
* current JST timestamp.

## Connectivity

* Binance public-stream state;
* Binance user-stream state;
* last market event age;
* last user-data event age;
* order-book synchronization state;
* reconnect count;
* current stream generation;
* API error count;
* server clock offset.

## Strategy

* current state;
* running, suspended, or halted status;
* warm-up progress;
* entry permission;
* cooldown remaining;
* last decision;
* last decision reason;
* last model update;
* model-health level.

## Per symbol

* symbol;
* reference price;
* best bid and ask;
* spread in JPY and basis points;
* barrier width;
* grid state;
* current barrier epoch;
* (N^+);
* (N^-);
* (\lambda^+);
* (\lambda^-);
* total intensity;
* directional probability;
* Gamma Capture volatility;
* TP-first probability;
* SL-first probability;
* unresolved probability;
* expected continuation value;
* signal upcrossings;
* signal downcrossings;
* churn count;
* active risk flags.

## Position

* base quantity;
* average entry price;
* current value in JPY;
* realized P&L in JPY;
* unrealized P&L in JPY;
* fees in JPY;
* maximum favorable excursion;
* maximum adverse excursion;
* current TP;
* hard SL;
* adaptive trailing level;
* holding duration.

## Orders

* active order count;
* entry-order status;
* protective-order status;
* last fill;
* last cancellation;
* unresolved orders;
* partial fills;
* remaining protected quantity.

## Portfolio risk

* available JPY;
* locked JPY;
* total exposure;
* per-symbol exposure;
* daily realized P&L;
* daily marked P&L;
* daily drawdown;
* daily loss-limit usage;
* number of consecutive losses;
* current risk mode;
* kill-switch state.

# 21. Telegram report types

Implement the following report classes.

## Startup report

Send after:

* BBGO initialization;
* exchange session initialization;
* market discovery;
* strategy restoration;
* stream readiness.

Example:

```text
🟢 Gamma Capture Bot Started

Mode: PAPER
BBGO: <version-or-sha>
Strategy: gammacapture
Session: binance
JPY markets: BTCJPY, ETHJPY
Public stream: connected
User stream: connected
Restored positions: 0
Risk state: normal
Time: 2026-07-14 09:10:22 JST
```

## Heartbeat

Send periodically while the application is running.

Defaults:

```yaml
telegram:
  heartbeatInterval: 5m
  idleHeartbeatInterval: 30m
```

The heartbeat should be concise and contain:

* status;
* uptime;
* active symbols;
* open positions;
* P&L;
* data age;
* risk state.

Coalesce unchanged idle heartbeats to avoid message flooding.

## Detailed periodic report

Default:

```yaml
telegram:
  detailedReportInterval: 1h
```

Include complete model and risk status.

## Trade report

Send immediately for:

* entry requested;
* entry accepted;
* partial fill;
* full fill;
* TP changed;
* SL changed;
* partial TP;
* exit requested;
* full exit;
* rejected order.

Include reason codes and model snapshot at decision time.

## Risk alert

Send immediately for:

* hard-stop trigger;
* daily loss halt;
* model invalidation;
* stale data;
* user-stream disconnect;
* unprotected position;
* balance mismatch;
* unresolved order;
* excessive slippage;
* position-size violation;
* emergency stop.

## Recovery report

Send when an unhealthy component recovers.

## Daily report

Send at a configurable JST time:

```yaml
telegram:
  dailyReportTime: "23:55"
  timezone: Asia/Tokyo
```

Include:

* opening and ending JPY equity;
* realized and unrealized P&L;
* fees;
* number of trades;
* wins and losses;
* maximum drawdown;
* maximum exposure;
* TP exits;
* SL exits;
* churn exits;
* time exits;
* model-health incidents;
* operational incidents.

## Shutdown report

Send for graceful shutdown with:

* reason;
* open positions;
* open orders;
* persistence status;
* final daily P&L;
* whether positions remain protected.

# 22. Telegram severity and delivery guarantees

Define severities:

```text
INFO
TRADE
WARNING
CRITICAL
RECOVERY
DAILY
```

Implement a bounded asynchronous notification queue.

Rules:

* trading callbacks must not wait for Telegram network I/O;
* critical messages must be retried with exponential backoff;
* ordinary heartbeats may be coalesced;
* duplicate warnings may be rate limited;
* dropped low-severity messages must increment a metric;
* critical message delivery failures must be logged and persisted;
* Telegram failure must never suppress an emergency exchange action.

Telegram messages must:

* escape formatting correctly;
* remain within Telegram message limits;
* split long reports cleanly;
* use a stable report ID;
* include JST timestamps;
* avoid leaking secrets;
* avoid dumping raw exchange payloads.

# 23. Telegram commands

Retain and integrate with BBGO’s existing private commands where supported:

* `/status`;
* `/position`;
* `/balances`;
* `/closeposition`;
* `/suspend`;
* `/resume`;
* `/emergencystop`.

Add strategy-specific commands using BBGO’s current interaction registration mechanism.

Suggested commands:

```text
/gcstatus
/gchealth
/gcmodel
/gccrossings
/gcrisk
/gcpnl
/gcorders
/gcpositions
/gcreport
/gcconfig
/gcpause
/gcresume
/gcflatten
/gckill
/gchelp
```

## `/gcstatus`

Return a concise runtime summary.

## `/gchealth`

Return connectivity, data age, model health, persistence, and risk health.

## `/gcmodel [symbol]`

Return:

* barrier width;
* crossing counts;
* intensities;
* posterior uncertainty;
* Gamma Capture volatility;
* TP-first and SL-first probabilities;
* expected continuation value;
* validation status.

## `/gccrossings [symbol]`

Return recent non-overlapping crossing events and signal upcrossing/downcrossing counts.

## `/gcrisk`

Return current risk consumption and active limits.

## `/gcpnl`

Return session and daily P&L in JPY.

## `/gcorders`

Return active, partially filled, rejected, and unresolved orders.

## `/gcreport`

Generate an immediate detailed report.

## `/gcpause`

Suspend new entries while preserving position protection.

## `/gcresume`

Resume only if:

* operator is authorized;
* streams are healthy;
* model is valid;
* risk state permits operation.

## `/gcflatten`

Cancel entry orders and close selected or all positions.

Require:

1. target selection;
2. displayed estimated impact;
3. explicit confirmation;
4. short-lived confirmation nonce.

## `/gckill`

Trigger the configured emergency-stop policy.

Require two-step confirmation:

```text
/gckill
CONFIRM <nonce>
```

Nonce requirements:

* cryptographically random;
* bound to authorized chat;
* bound to requested action;
* expires within 30–60 seconds;
* usable once;
* persisted in audit log;
* never reusable after restart.

Read-only commands may run directly from immutable snapshots.

State-changing commands must be dispatched into a serialized strategy control channel. Do not execute exchange mutations concurrently from the Telegram handler goroutine.

# 24. Telegram command audit log

Persist every command attempt:

```go
type OperatorCommandAudit struct {
    ID              string
    ReceivedAt      time.Time
    ChatIDHash      string
    UserIDHash      string
    Command         string
    Arguments       string
    Authorized      bool
    ConfirmationID  string
    Result          string
    ErrorCode       string
    CompletedAt     time.Time
}
```

Do not persist unnecessary personal Telegram information.

Record:

* authorization failure;
* validation failure;
* accepted command;
* confirmation;
* execution result;
* affected orders and positions;
* before-and-after strategy state.

# 25. Prometheus metrics

Use BBGO’s existing metrics server and conventions.

Add strategy metrics with bounded labels such as strategy instance and symbol.

Required metrics include:

```text
bbgo_gammacapture_upcrossings_total
bbgo_gammacapture_downcrossings_total
bbgo_gammacapture_signal_upcrossings_total
bbgo_gammacapture_signal_downcrossings_total
bbgo_gammacapture_churn
bbgo_gammacapture_lambda_up
bbgo_gammacapture_lambda_down
bbgo_gammacapture_gc_volatility
bbgo_gammacapture_tp_first_probability
bbgo_gammacapture_sl_first_probability
bbgo_gammacapture_unresolved_probability
bbgo_gammacapture_expected_value_jpy
bbgo_gammacapture_position_jpy
bbgo_gammacapture_realized_pnl_jpy
bbgo_gammacapture_unrealized_pnl_jpy
bbgo_gammacapture_daily_drawdown_jpy
bbgo_gammacapture_model_health
bbgo_gammacapture_market_data_age_seconds
bbgo_gammacapture_user_data_age_seconds
bbgo_gammacapture_unresolved_orders
bbgo_gammacapture_telegram_queue_depth
bbgo_gammacapture_telegram_send_failures_total
bbgo_gammacapture_telegram_messages_dropped_total
```

Avoid unbounded labels such as:

* order ID;
* client order ID;
* error message;
* Telegram username;
* arbitrary reason text.

# 26. Persistence

Use BBGO-compatible persistence.

Production preferences:

* MySQL for synchronized trading data where supported by the pinned BBGO version;
* Redis or BBGO JSON persistence for strategy state;
* SQLite only for local development and tests when compatible.

Persist:

* barrier anchor;
* barrier width;
* barrier epoch;
* current grid state;
* crossing history required for restoration;
* intensity posterior parameters;
* Doob signal state;
* churn counters;
* strategy controller state;
* cooldown state;
* current position;
* TP and SL state;
* high-water mark;
* last decision;
* risk counters;
* daily P&L checkpoints;
* notification delivery state;
* pending operator confirmation state where appropriate.

Use `bbgo.Sync` or the current pinned equivalent after material state transitions.

On restart:

1. load persisted state;
2. query account balances;
3. query open orders;
4. reconcile fills;
5. reconcile BBGO position state;
6. check protective orders;
7. block trading while mismatches remain;
8. send a Telegram recovery report.

# 27. Order execution

Use BBGO’s order executor and active-order abstractions where appropriate.

Implement:

* deterministic idempotent client order identifiers;
* partial-fill handling;
* cancellation-race handling;
* duplicate-event protection;
* commission accounting;
* balance reconciliation;
* unknown-order-state recovery;
* protective-order replacement;
* startup order recovery.

Never assume a request timeout means the order failed.

Protect each filled quantity as soon as practical.

When native Binance protective order lists are available and supported through the pinned BBGO adapter, use them where they correctly express the required protection.

Otherwise:

* implement synthetic TP/SL supervision;
* retain an exchange-native hard stop when practical;
* do not leave a position unprotected during cancel-and-replace;
* handle stop-limit non-fill risk;
* support an emergency marketable exit under configured slippage limits.

# 28. Risk controls

Implement:

* maximum risk per trade;
* maximum JPY amount per symbol;
* maximum total JPY exposure;
* maximum number of positions;
* maximum correlated exposure;
* maximum daily realized loss;
* maximum marked-to-market drawdown;
* maximum consecutive losses;
* maximum spread;
* maximum slippage;
* maximum market-data age;
* maximum user-data age;
* maximum API error rate;
* maximum order acknowledgement latency;
* maximum model age;
* maximum holding time;
* cooldown after stop;
* cooldown after churn;
* per-symbol kill switch;
* global kill switch.

Risk checks must run:

* before order submission;
* after every fill;
* after every balance update;
* after every relevant model update;
* periodically while positions exist.

Risk decisions override strategy decisions.

# 29. Backtesting and replay

BBGO’s standard backtesting is often candle-oriented, but this model requires event-level barrier crossings.

Implement an event-replay extension compatible with the same production strategy logic.

Do not maintain separate simplified live and backtest decision code.

Replay must support:

* trades;
* bid and ask changes;
* depth changes where available;
* user-data events;
* latency simulation;
* spread;
* fees;
* partial fills;
* slippage;
* missed data;
* duplicate events;
* disconnects;
* stop-limit non-fills.

If standard BBGO backtesting cannot reproduce event-level crossings, create an isolated replay driver or mock BBGO exchange/session adapter without rewriting the strategy.

# 30. Testing

Provide unit, integration, replay, and property-based tests.

Test:

* barrier crossings;
* multi-barrier moves;
* gaps;
* epoch resets;
* signal upcrossings;
* signal downcrossings;
* churn;
* posterior intensities;
* Skellam probabilities;
* first-passage probabilities;
* Monte Carlo agreement;
* decimal rounding;
* market filters;
* position accounting;
* partial fills;
* duplicate fills;
* unknown order status;
* hard-stop precedence;
* adaptive TP;
* daily loss halt;
* strategy suspend and resume;
* emergency stop;
* Telegram authorization;
* confirmation expiry;
* duplicate Telegram commands;
* notification queue overflow;
* Telegram outage;
* restart recovery.

Required invariants:

* spot inventory never becomes negative;
* no sell exceeds owned inventory;
* probability values remain in ([0,1]);
* (q_t+s_t+u_t\approx1);
* hard stop cannot be overridden;
* halted strategy cannot enter;
* each fill is counted once;
* each completed upcrossing is counted once;
* Telegram failure cannot block risk execution;
* unauthorized Telegram users cannot mutate state;
* repeated emergency commands remain idempotent.

# 31. Example BBGO configuration

Produce a working example similar to:

```yaml
sessions:
  binance:
    exchange: binance
    envVarPrefix: BINANCE

persistence:
  redis:
    host: redis
    port: 6379
    db: 0

exchangeStrategies:
  - on: binance
    gammacapture:
      environment: paper

      symbolSelection:
        mode: dynamic
        quoteAsset: JPY
        allowlist: []
        denylist: []

      referencePrice:
        mode: microprice
        maxAge: 2s

      barrier:
        mode: adaptive
        epochDuration: 30m
        updateOnlyWhenFlat: true
        minSpreadMultiple: 3.0
        reversalBuffer: 0.25

      intensity:
        window: 30m
        priorAlphaUp: 1.0
        priorBetaUp: 60.0
        priorAlphaDown: 1.0
        priorBetaDown: 60.0

      horizon:
        prediction: 15m
        maximumHolding: 30m

      signal:
        lowerThreshold: 0.40
        upperThreshold: 0.60
        entryProbability: 0.62
        exitProbability: 0.42
        maxChurn: 4

      takeProfit:
        activationBarriers: 2
        initialTargetBarriers: 5
        minTrailingBarriers: 1
        maxTrailingBarriers: 4

      stopLoss:
        hardStopBarriers: 3
        confirmationCrossings: 2

      risk:
        maxRiskPerTradeJPY: 1000
        maxSymbolNotionalJPY: 50000
        maxTotalNotionalJPY: 100000
        maxDailyLossJPY: 5000
        maxDrawdownJPY: 7500
        maxOpenPositions: 2
        maxSpreadBps: 20
        cooldownAfterStop: 30m

      telegram:
        enabled: true
        heartbeatInterval: 5m
        idleHeartbeatInterval: 30m
        detailedReportInterval: 1h
        dailyReportTime: "23:55"
        timezone: Asia/Tokyo
        reportTrades: true
        reportModelChanges: true
        reportRiskWarnings: true
        allowOperatorCommands: true
```

Confirm all field names against the implementation and provide validation errors for invalid combinations.

# 32. Deliverables

Produce:

1. Architecture document.
2. Mathematical specification.
3. BBGO integration design.
4. Strategy state diagram.
5. Telegram reporting specification.
6. Telegram command and authorization specification.
7. Repository tree.
8. Complete compilable Go implementation.
9. BBGO configuration examples.
10. `.env.example`.
11. MySQL migrations where required.
12. Redis or JSON persistence setup.
13. Docker and Docker Compose files.
14. Event replay implementation.
15. Unit tests.
16. Integration tests.
17. Property-based tests.
18. Python validation notebooks.
19. Prometheus metrics.
20. Grafana dashboard.
21. Telegram report samples.
22. Operational runbook.
23. Emergency-stop runbook.
24. Restart-recovery runbook.
25. Security checklist.
26. Model-risk document.
27. Backtest report template.
28. Live-deployment checklist.

# 33. Development sequence

Build in this order:

1. Pin and inspect BBGO.
2. Document BBGO extension points.
3. Implement configuration validation.
4. Implement market discovery.
5. Implement deterministic barrier counting.
6. Implement model state and persistence.
7. Implement Gamma Capture volatility.
8. Implement intensity estimation.
9. Implement Skellam calculations.
10. Implement finite-horizon first passage.
11. Implement Doob-inspired signal state.
12. Implement immutable runtime snapshots.
13. Implement Telegram read-only reports.
14. Implement Prometheus metrics.
15. Implement entry and exit decisions.
16. Implement BBGO position and order handling.
17. Implement risk controls.
18. Implement Telegram state-changing commands.
19. Implement replay.
20. Implement paper trading.
21. Implement restart reconciliation.
22. Run shadow mode.
23. Review Telegram and operational security.
24. Enable live trading only after all gates pass.

# 34. Live-trading acceptance gates

Live trading must remain disabled until:

* deterministic replay tests pass;
* no-lookahead audit passes;
* model probability calibration passes;
* Poisson or alternative-model diagnostics pass;
* market-filter tests pass;
* partial-fill tests pass;
* duplicate-event tests pass;
* restart-recovery tests pass;
* unknown-order-state tests pass;
* hard-stop tests pass;
* stale-data tests pass;
* Telegram-outage tests pass;
* unauthorized-command tests pass;
* emergency-confirmation tests pass;
* paper-trading period completes;
* risk limits remain within predefined tolerances;
* operator explicitly enables live mode.

Begin by producing:

1. BBGO compatibility assessment.
2. Architecture.
3. Mathematical assumptions.
4. Failure-mode analysis.
5. Strategy state diagram.
6. Telegram report schema.
7. Telegram command flow.
8. Configuration schema.
9. Repository layout.
10. Implementation milestones.

Then implement the system module by module. Do not skip reconciliation, persistence, tests, Telegram security, or paper trading to produce a faster prototype.

