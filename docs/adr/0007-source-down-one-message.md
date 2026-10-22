# 0007. A source that is down is one message, and a run is one message

Date: 2026-10-22. Status: accepted.

## Context

`serve` posts one message per check that changes. A day of `serve` on a laptop
against several MongoDB databases and a ClickHouse, through a VPN and a tunnel,
showed what that costs when the network is not perfect:

- The first start posted 9 messages in a row, one per check that was not clean.
- The tunnel to ClickHouse went down: 4 messages, "payments_reach_stats failed:
  right: source stats: clickhouse: dial tcp ...: connection refused", and the same
  for the three other checks of that source.
- An hour later the VPN went down: 9 more. Each one carried the error of the
  MongoDB driver, which prints the whole topology it tried: a few hundred
  characters of addresses and server states per message.
- After that `serve` waited a full `interval` before trying again. A VPN that is
  back in two minutes still leaves an hour without checks.

The checks were fine; the network was not. Thirteen messages said one thing: two
sources could not be reached. A channel that gets thirteen messages for one event
is muted by the end of the week, and then the message about a new double charge is
not read either.

Opening a source already talks to the server: MongoDB runs `connectionStatus`,
PostgreSQL pings, ClickHouse reads the `readonly` setting. A source that cannot be
reached fails there, once per run, and every check of the source fails with the
same error. What is missing is the part that tells people about it.

## Decision

**A source is down when it cannot be opened, or when a query fails because the
connection failed.** A connection failure is what the driver reports as one: a
network error, a MongoDB server selection error, a refused or reset TCP
connection. A timeout of the query itself, a syntax error or a failed `expect`
are failures of the check, as before: they say something about the check, and a
person has to look at it.

When a query finds the source down, the checks of that source that have not run in
this cycle are not run: they would wait for the same timeout and fail the same
way.

**A check that fails because its source is down keeps its violations.** Its result
is `failed`, as any run that did not say what it sees, with the error "source shop
is down: ...". The set of violations from ADR 0006 is not touched, so nothing is
posted as fixed while the source is away, and nothing is posted as new when it is
back, unless it is new. `money_check_up` is 0 for these checks, as for any failed
run.

**One line per source.** The message says "source shop is down, 9 checks did not
run: <the error, cut to 200 characters>", once, when the source goes down, and
"source shop is up again" when it comes back. The checks of that source get no lines
of their own for failing and running again. The full error goes to the log. A
`compare` check whose left or right source is down is counted with that source.

A new gauge, `money_check_source_up{source}`, is 1 when the source opened and
answered in the last cycle that used it, 0 when it was down. The alert "a source
is down for 15 minutes" takes the place of one alert per check.

**One message per cycle.** Every change of a cycle goes into one message: a
first line with the counts, then one line per check or source, with the same words
as today.

```
money-checks: 2 checks changed, 1 source down
source stats is down, 4 checks did not run: dial tcp 127.0.0.1:18123: connect: connection refused
two_renewals_in_one_period found 1 new violation (19.99 EUR), 3 in all [error]
two_active_subscriptions: 1 violation fixed (9.99 EUR), 2 left [error]
```

A cycle with no changes posts nothing, as today. The lines are sorted: sources
first, then checks in the order of the configuration. Above 20 lines the message
ends with "and N more" and the log has all of them: a message that needs scrolling
is not read either. The first start, without a state file, is one message with a
line per check that is not clean, instead of a message per check.

**A source that is down is tried again sooner.** After a cycle in which a source
was down, `serve` runs the checks of that source again after `serve.retry`, 1m by
default, then 2m, 4m and so on up to `interval`. A retry runs only those checks, in
the scope they would have had, and does not move the times of `interval`, `every`
and `full_every`. A retry that finds the source up posts "up again" and whatever
the checks found.

## Why not keep one message per check

A message per check lets a Slack thread or a filter follow one check. Nobody asked
for it, and the cost is real: the lines of one cycle are related (a source, a
deploy, a night of batch jobs), and they read better together. A tool that needs a
message per check can read `/metrics`, which has a series per check.

## Why not a new status for "source down"

`skipped` or `unreachable` next to `ok`, `violations` and `failed` would say more in
one word, but every reader of the JSON report and every script that looks at the
exit code of `run` would have to learn it. A check whose source is down did not
run, and `failed` already means that. The difference is in the error text and in
`money_check_source_up`.

## Consequences

- A query that fails with a network error halfway through a cycle stops the rest of
  the checks of its source for that cycle. If the network error was only that one
  connection, those checks run on the retry a minute later.
- Telling a connection failure from other errors depends on what each driver
  returns. An error that is not recognised stays the failure of the check: it is
  posted per check, which is the behaviour of today, not a silence.
- The cut error in Slack may hide the cause. The log has the full text, with the
  name of the source, and the message says which source to look for.
- `run` does not change: it has no cycle and no message. Its report already shows
  the error of each check, and the exit code is 2 when a check failed.
- A retry adds load only on the sources that were down, and only their checks run.
  With `every`, a heavy check that failed because its source was down is retried
  like the others: it did not run, so its turn is not used up.
