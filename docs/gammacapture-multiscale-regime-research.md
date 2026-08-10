# Gamma Capture: one-minute data for long-horizon regime control

Status: research only. Nothing in this note is enabled in the live strategy.

## Questions that must be falsified first

1. Does one-minute executable BBO data predict the sign of a three-hour move,
   or does it only improve the estimate of current volatility and regime age?
2. Can a current-regime posterior be calibrated into a future competing
   first-passage probability without treating overlapping horizons as
   independent samples?
3. How much of apparent one-minute variation is spread/microstructure noise or
   an isolated jump rather than persistent drift?
4. Can a no-pretraining online learner update quickly enough when at most eight
   independent three-hour labels mature per complete day?
5. Does a direction model add information beyond a causal expanding-window
   climatology after fees and executable bid/ask barriers are used?

## Hard blockers

- A spot-only, self-financing strategy has `dV = q dS - costs`, with `q >= 0`.
  It cannot guarantee positive P&L through an unanticipated downward jump.
  The achievable objective is lower exposure before/through a statistically
  detectable fall plus spread capture, not pathwise positive P&L on every fall.
- The local ETHJPY archive spans about fifteen days. Millions of BBO updates do
  not create millions of independent three-hour outcomes. Non-overlapping
  labels provide only about 101 resolved observations in the current sample.
- Public BBO and aggregate trades do not identify private fills or queue
  priority. Standalone direction tests therefore precede, but cannot replace,
  the execution backtest.
- A posterior for the sign of the current one-minute regime is not a calibrated
  probability that the down barrier will arrive before the up barrier over the
  next three hours.

## Literature-derived design boundaries

- Corsi's HAR-RV model aggregates heterogeneous horizons parsimoniously and is
  appropriate for forecasting persistent realized variance. It does not by
  itself establish return-direction predictability:
  <https://doi.org/10.1093/jjfinec/nbp001>.
- MIDAS regressions are the appropriate family for mapping high-frequency
  predictors to a lower-frequency conditional moment without pretending the
  sampling frequencies are identical:
  <https://doi.org/10.1080/07474930600972467>.
- Realized bipower variation separates continuous variance from rare jumps;
  realized kernels and pre-averaging address market-microstructure noise:
  <https://www.nuff.ox.ac.uk/economics/papers/2003/w18/eric_may03.pdf>,
  <https://ssrn.com/abstract=620203>, and
  <https://ssrn.com/abstract=1150685>.
- Adams and MacKay's Bayesian online changepoint detection maintains a causal
  posterior over current run length:
  <https://arxiv.org/abs/0710.3742>. Tsaknaki, Lillo, and Mazzarisi apply the
  idea to persistent financial order-flow regimes and explicitly distinguish
  online regime identification from market-impact prediction:
  <https://arxiv.org/abs/2307.02375>.
- Cont, Kukanov, and Stoikov find short-interval price changes related to order
  flow imbalance and inversely related to depth. That is evidence for an
  immediate microstructure feature, not permission to extrapolate the feature
  three hours without a separate out-of-sample test:
  <https://arxiv.org/abs/1011.6402>.
- Daniel and Moskowitz study monthly cross-sectional long-short momentum, not
  a single-asset intraday spot strategy. The transferable result is to estimate
  conditional mean and variance separately and scale risky exposure as
  `w* = mu / (2 lambda sigma^2)`. Their bear-state/high-volatility interaction
  also warns that continuation exposure can have crash-like rebound risk. Their
  24-month/126-day windows and fitted coefficients are not transferable:
  <https://www.kentdaniel.net/papers/published/jfe_16.pdf>.

## Standalone v1 model

`BayesianMultiscaleRegime` consumes one closed UTC-minute executable BBO at a
time. Ask and bid log returns must agree in sign; otherwise spread movement
contributes zero directional evidence. Bipower variation supplies a causal
jump-robust scale. A truncated Normal-Inverse-Gamma/Student-t Bayesian online
changepoint filter estimates current run length, regime mean, uncertainty,
change probability, continuous volatility, jump fraction, and `mu/sigma^2`.
It has no artifact and starts from symmetric priors.

Pure unit tests require:

- high down posterior for a seeded persistent down process;
- an 80% up posterior within fifteen minutes of a strong reversal;
- price-scale invariance;
- no false direction from spread-only bid/ask disagreement; and
- a causal reset after a missing minute.

All five tests pass. These tests establish implementation behavior, not market
predictive accuracy.

## Real-data v1 rejection

The standalone evaluator used ETHJPY BBO from 2026-07-23 through 2026-08-07.
Predictions were spaced three hours apart. A label was down-first only when a
future ask fell far enough below the current bid to clear the full 20-bps round
trip; up-first used the symmetric current-ask/future-bid barrier. Same-minute
ties and horizons touching neither barrier were censored. Online calibration
could use a label only after its complete three-hour horizon elapsed.

There were 104 predictions: 48 down-first, 53 up-first, and three censored.

| BOCPD hazard mean | raw Brier | raw skill vs causal climatology | raw accuracy | calibrated Brier | calibrated skill |
|---:|---:|---:|---:|---:|---:|
| 60 min | 0.3337 | -28.62% | 45.54% | 0.2804 | -8.07% |
| 180 min | 0.3287 | -26.69% | 49.50% | 0.2655 | -2.34% |
| 360 min | 0.3262 | -25.71% | 51.49% | 0.2798 | -7.83% |

For the 180-minute variant, the 95% Wilson lower bound on raw accuracy was
39.95%. Daily performance changed sign repeatedly. The current-regime
posterior therefore has no standalone evidence of three-hour first-passage
skill and must not be integrated or backtested for P&L.

## Next research candidate and acceptance gate

The next candidate is a causal mixed-frequency conditional-moment layer, not a
hard-coded inversion of the failed signal:

1. Continue estimating noise/jump-aware one-minute state and run length.
2. Form a small, predeclared feature vector: current-regime log odds,
   long/short realized-variance ratio, changepoint probability, and the
   bearish-state-by-volatility interaction motivated by Daniel-Moskowitz.
3. When a non-overlapping executable first-passage label matures, update a
   regularized Bayesian/score-driven logistic competing-risk model online.
   No future label and no pretrained artifact is available at startup.
4. Estimate conditional executable return magnitude and variance separately.
   Only after calibration succeeds may `mu/sigma^2` become a bounded Macro
   inventory tilt.
5. Freeze the feature definition before testing. Require positive Brier skill
   and log-loss improvement versus causal climatology, calibration error below
   0.10, no single date supplying the entire improvement, and stability across
   the predeclared 60/180/360-minute hazard sensitivity set.
6. Only a candidate passing those standalone gates may be wired into Macro and
   compared with QV-only in the next-BBO execution backtest.

## v2 online conditional-outcome result

The predeclared v2 layer used a symmetric zero-mean Gaussian coefficient prior
and updated a Bayesian logistic approximation only after each label matured.
Its fixed features were current-regime down log odds, causal relative
volatility stress, and the bearish-log-odds-by-positive-volatility interaction
motivated by Daniel-Moskowitz. It could learn either continuation or reversal;
neither sign was hard-coded.

At the three-hour horizon, the 180-minute state hazard reduced calibration
error from 0.272 to 0.084, but conditional-outcome Brier skill was still
-3.31% and log loss was 0.7304 versus causal climatology 0.7126. This model is
also rejected.

Because published order-flow BOCPD regimes are often only 7–15 minutes long,
the same frozen construction was then tested at 15, 30, and 60 minutes before
attempting any multi-step survival aggregation:

| outcome horizon | resolved labels | best online-outcome Brier skill | conclusion |
|---:|---:|---:|---|
| 15 min | 445 | -0.60% | reject |
| 30 min | 355 | -0.70% | reject |
| 60 min | 261 | -1.41% | reject |
| 180 min | 101 | -2.81% | reject |

Every horizon's online-outcome log loss was worse than its causal climatology;
all 95% Wilson accuracy lower bounds remained below 50%. Consequently there is
no justified short-hazard model to aggregate into a three-hour survival law.

The evidence supports a narrower use of one-minute data: update jump/noise-aware
conditional variance and changepoint uncertainty faster, then reduce exposure
through an existing independently justified conditional-mean signal. It does
not support letting this price-only state choose bullish versus bearish Macro
