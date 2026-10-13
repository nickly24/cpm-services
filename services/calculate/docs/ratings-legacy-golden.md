# Rating math parity

The checked-in vectors in `internal/calculate/testdata/ratings_legacy_golden.json`
were generated on 2026-10-09 with **CPython 3.12.14**, the interpreter at
`dev/.venv/bin/python`. This is the dev legacy runtime recommended in
`dev/README.md`. The monolith repository does not contain an interpreter pin or
Dockerfile, and its `requirements.txt` does not specify Python's version. These
vectors establish parity with that observed dev runtime; the old production
interpreter version has not been verified.

Reproduce the vectors from the repository root:

```sh
dev/.venv/bin/python services/calculate/scripts/ratings_legacy_golden.py
```

The generator runs the actual homework/test/final functions from
`cpm-back-main/cpm_back/services/exam/calculate_ratings.py` with synthetic
in-memory SQL cursors and Mongo collections. It compiles the actual pure
`calculate_from_snapshot` function from `cpm_back/services/exams/rating.py`
without importing the application's configuration or DB modules. It does not
open a database, use credentials, or contact a server. SQL owner rows are
already selected for the requested period and Moscow day, just as the snapshot
owner supplies them to Calculate.

The ordinary Go test reads these static vectors without requiring Python. It
compares the IEEE-754 bits of all component ratings and the final rating. Four
vectors cover repeated fractional scores, equally weighted fractional direction
means, external null/zero scores, and mixed internal/external Unicode direction
labels. Review both the vectors and runtime provenance when regenerating them
with a different Python version.

## Fractional scores

The legacy homework function accumulates scores using `total_score += score`.
The test function, directional average, and exam snapshot function use Python's
`sum()`. Since Python 3.12, float `sum()` uses compensated addition; see the
[official CPython 3.12 implementation](https://github.com/python/cpython/blob/3.12/Python/bltinmodule.c).
Calculate uses the same Neumaier accumulation for those three sums and keeps
ordinary addition for homework. For ten scores of `0.1`, the homework mean is
`0.09999999999999999`; test and exam means are `0.1`. This distinction is
observable before rounding and is covered by the vectors.

The final formula retains its exact operation order:

```text
(homework * 25) / 100 + exams * 6 + (tests * 45) / 100
```

There is no clamping, redistribution of absent components, or aggregate
rounding. Nonfinite source values or an overflowing result cannot publish a
ready result.

The three weighted components are explicitly converted to `float64` before
addition so their intermediate rounding matches Python. This prevents the
Go compiler from fusing `exams * 6` with an addition and silently removing that
rounding; the rule is defined in the
[Go specification](https://go.dev/ref/spec#Floating_point_operators).

## Snapshot responsibilities and date tests

`ratings_math_audit_test.go` checks both inclusive period endpoints, Moscow
midnight immediately before/at/after the transition, leap day and calendar year
boundaries, and the strictly overdue homework rule. Homework's `date_pass`
remains detail metadata; eligibility depends on its deadline, exact type `ОВ`,
period and the frozen Moscow day of `as_of`.

Calculate's personal test DTO deliberately has no start/date field. The Tests
owner selects published active internal tests by Moscow start day and external
tests by calendar date in the inclusive period. Legacy includes future internal
tests and external tests within the period. Calculate retains those selected
tests, counts missing results as zero, retains a completed zero, and rejects
queued/processing/retrying/failed internal calculations as pending. Owner tests
must verify date selection and source completeness.

New test details include `calculation_status: "ready" | "missing"` so a
completed zero remains distinguishable from a missing result even though both
have detail `score: 0`. A personal input with status `missing` and a nonnull
score is contradictory and rejected. Historical cached details that lack this
additive field remain readable; replay preserves its absence rather than
guessing a status from their zero score.

The Exams owner selects classic exams that have started by `as_of`, outside LMS
exams by inclusive calendar date, and the effective completed attempt with its
latest appeal. Calculate receives one effective row per exam, rejects duplicate
exam IDs in one personal snapshot, preserves attempt/appeal metadata, and uses
the supplied score even when a retake is worse. Missing and completed-zero
exams both contribute zero but retain distinct detail statuses. Shared exam
and test IDs in different personal organization/division snapshots remain
independent; a copied exam has its own ID and missing result until graded.
Parallel math tests cover separate divisions and accounts with the same shared
IDs. These tests verify immutable personal aggregation, not owner persistence
or Rabbit delivery.
