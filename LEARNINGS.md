# LEARNINGS

This document captures the design journey of **asmt**, a lightweight server
monitoring dashboard for a single Linux machine. It's written for a general
audience: leadership, recruiters, or anyone curious about the reasoning
behind the project's architecture. Technical concepts are explained in
plain business terms.

---

## The goal

Build a **monitoring dashboard for a single server** that is:

- **Fast to install and forget** — one command, auto-detects everything, runs
  as a lightweight background service.
- **Tiny** — typical memory footprint under 15 MB, with 50 MB as a hard
  ceiling even under heavy load.
- **Honest** — the dashboard should reflect the *real* state of the machine,
  not a simplified version that might be wrong.
- **Time-saving** — the single most important feature is helping an operator
  answer "what went wrong and when?" in seconds, not minutes.

I chose **Go** as the implementation language because it compiles to a single
static binary (no dependency hell), starts in milliseconds, and gives us
tight memory control. A Go-based tool with an embedded web dashboard can
easily sit under 15 MB, which is lighter than almost every alternative in
this space.

---

## The problem: Linux keeps excellent logs, but they're scattered

Linux and its ecosystem already have **outstanding logging built in**.
Every piece of software on a typical server writes to its own log file:

- The web server (`nginx`, `apache`) writes to `/var/log/nginx/error.log`
- The PHP runtime writes to `/var/log/php-fpm/error.log`
- The database writes to `/var/log/mysql/error.log`
- The kernel writes OOM-killer and disk errors to `/var/log/kern.log`
- `systemd` captures almost everything else in its "journal"

Each log is **authoritative** — it's the source of truth, written by the
software that experienced the event, with accurate timestamps.

The problem isn't that logs *don't exist*. The problem is that **a human
investigating an incident has to know where each log lives, how to read
each format, and how to correlate events across all of them at once**.
That's a skill that takes years to build. Most operators spend precious
incident-response minutes just grepping through five different files trying
to piece the story together.

**asmt's real value proposition is time savings during incidents.** It
knows where every source of truth lives, pulls them into one place, and
renders a visual timeline anyone can read in seconds.

---

## The key philosophical shift

We went through several iterations of how the dashboard should relate to
its data sources. The learning is worth writing down because it applies to
many other tools as well.

### Iteration 1: Be the source of truth (wrong)

The first instinct was to have our tool **record everything itself**. When
a check found disk usage at 86%, we'd write that observation to our own
internal log buffer. Clicking a red day in the history bar would pull from
our buffer.

This felt clean — one database, one API, one display. But it had serious
problems:

1. **Our buffer is only as good as our uptime.** If the tool crashed or
   was restarted, any events we didn't flush to disk were lost.
2. **We were second-guessing a system that already does this well.**
   The OS journal is durable, timestamped, and survives reboots. Why
   pretend we had a better source than it does?
3. **Our history file became the "source of truth" even though it was
   actually our own cached interpretation.** If our cache was wrong (bug,
   corruption, file corruption from a power loss), our dashboard was wrong.

### Iteration 2: Fake historical data (very wrong)

To paper over the gaps, I added a feature that created **synthetic log
entries** for historical days based on our history cache. "Oh, disk was
warn on Tuesday? I'll add a fake entry at noon Tuesday that says 'disk
warn.'"

This was worse. I was **inventing data that never happened**, placing it
at fictional timestamps, and presenting it to the user as real. If the
user clicked a red day and landed on "noon" entry, they had no way to know
that "noon" was made up.

The lesson: **never fabricate data to fill a visual gap**. A missing bucket
in the UI is information. Pretending it isn't missing is a lie.

### Iteration 3: The authoritative-source-first design (right)

The correct mental model:

> **Our tool is a dashboard on top of existing truth, not a replacement
> for it. Its job is to make the OS's own data easier to see and act on.**

Concretely:

- **The OS journal and log files are always the source of truth.**
  We never pretend to have better data than they do.
- **Our buffer is a cache**, not a database. If it loses data, the OS
  still has the original — we just have to re-read it.
- **When the user clicks a historical day, we don't invent data** — we
  either show real captured events, or we show nothing and offer to
  re-sync from the source.
- **Our check observations are explicitly labeled as "observations"**, not
  facts. A chip saying "disk warn × 143" is our interpretation of a
  periodic measurement. A chip from the journal saying the same thing
  is an authoritative system-level record. The user can see which is
  which at a glance.

This shift changed the entire mental model of the project. Our tool stopped
trying to BE a log system and became a **smart lens** on the logs that
already exist.

---

## The crisis: what happens when a server is under attack?

Every engineer's first instinct when building a dashboard is to handle
the "happy path" — the normal, calm operation. But monitoring tools have
a peculiar burden: **they must work best exactly when everything else is
failing.** If a server is being overwhelmed, that's the moment its monitor
needs to be rock-solid.

Early versions of our tool didn't think about this. Every event was stored
as an individual entry in a growing list. For a quiet server, that's fine.
For a server under a DDoS attack producing **500 error log entries per
second**, we're looking at 43 million entries per day. No bounded list can
hold that.

### The solution: aggregation by time bucket (in plain English)

Think of the log buffer like a **sales receipt for a busy store**:

Imagine a shop that sells 500 identical loaves of bread between 9:00 AM
and 9:15 AM. Two ways to record it:

**Approach A** (naive): Print 500 individual receipts, one per loaf. If
business keeps up, by the end of the day you have 200,000 receipts. You
need a warehouse for receipts.

**Approach B** (aggregation): Print one receipt for that 15-minute window
that says "Sold: 500 × bread between 9:00 AM and 9:15 AM." You know
exactly what happened — the type of item, the time window, the quantity.
You lose the exact minute-by-minute breakdown, but you save 499 receipts.

Our tool uses Approach B. **One entry per (15-minute window × error
type × source)**, with a counter that tracks how many individual events
it represents.

The memory impact is dramatic:

| Scenario | Raw events per day | Aggregated entries | Memory used |
|---|---|---|---|
| Quiet server | ~100 | ~20 | ~15 KB |
| Busy web server | ~10,000 | ~500 | ~350 KB |
| Server under attack | 43,000,000 | ~2,000 | ~1.4 MB |

**A server under attack uses essentially the same memory as a quiet one.**
The count field in the existing entries just goes up.

This single design choice lets the tool sit quietly at ~9 MB for months
without ever flinching — even when the server it's monitoring is
experiencing the worst day of its life.

### Verification

The aggregating buffer was load-tested to confirm it actually holds up
under the advertised target (500 events per second for 24+ hours). A
simulated 24-hour run of 43.2 million events was processed in 21
seconds — a throughput of 2 million events per second — and the buffer
remained at about 2,000 entries with no measurable memory growth. The
target is 500/second; the tool handles about 4,000× that before becoming
the bottleneck.

---

## Design principles (final iteration)

These are the rules the tool follows today. Each one is a lesson learned
the hard way during development.

1. **Never trust our own data more than the OS's.**
   Our buffer is a dashboard cache. The system journal, log files, and
   kernel log are always the authoritative source. When the two disagree,
   assume we're wrong.

2. **Never fabricate data to fill gaps.**
   Missing data is information. A blank spot on the timeline tells the
   user "we don't know what happened here" — that's honest and actionable.
   Inventing entries is worse than showing nothing.

3. **Every displayed event points to its origin.**
   Check observations are labeled as observations. Journal entries are
   labeled as journal entries. The user can always tell whether they're
   looking at an interpretation or a raw record.

4. **Never trust user input directly.**
   Any data that comes from a web request is validated strictly before
   being used — especially before being passed to a system command. Dates,
   for example, must match an exact YYYY-MM-DD pattern before we even
   attempt to parse them.

5. **Every subprocess call is bounded.**
   When we ask the operating system to do work for us (like querying the
   journal), we set a hard time limit, a maximum output size, and a
   maximum number of lines. A misbehaving subprocess cannot take down
   the tool, no matter what it returns.

6. **Graceful degradation is mandatory.**
   If the system journal isn't available (Alpine, older distros), the
   dashboard still works — it just relies on the log files it can tail
   directly. No feature should require the best-case environment.

7. **Stream, don't accumulate.**
   When processing large volumes (like syncing a week of journal events),
   we read one line, act on it immediately, and throw it away. We never
   load the whole thing into memory first. This is what makes it safe to
   sync a day of journal data that might contain millions of lines.

8. **Aggregate on write, not on read.**
   Similar events are combined into a single "summary entry" the moment
   they arrive, not when the dashboard asks for them. This keeps memory
   bounded by the number of distinct error types, not by the event rate.

9. **Optimize the ceiling for the worst case; optimize usage for the
   typical case.**
   Memory ceilings exist for incidents, not for normal operation. A
   generous limit (we use 64 MiB) costs nothing when idle — the tool
   still sits at ~9 MB — but gives the runtime headroom when the
   monitoring tool itself is under the most pressure. The rule:
   **prepare for the bad day, not the average one.**

10. **Single-flight expensive operations.**
    If the user clicks "Sync" ten times in a row, only one sync runs.
    The other nine calls immediately return "already in progress" without
    queuing more work. Busy users should not be able to accidentally DoS
    their own monitor.

11. **Our tool should supplement, not replace.**
    This is the meta-principle that encapsulates all the others. Linux
    already has world-class logging. Our job isn't to rebuild it — it's
    to know where every piece lives and assemble it into one readable
    view, so the operator can spend their incident-response time *acting*
    instead of *searching*.

---

## The memory story (in business terms)

One of the most educational moments of the project was a debate about how
generous the memory ceiling should be.

Early on, I tuned the limit aggressively — 10 MiB, then 12 MiB. "Smaller
is better, right?" The result: under normal conditions, the tool worked
fine. But when I ran a stress test (pulling a week of journal data), the
runtime thrashed, the sync took minutes instead of seconds, and memory
briefly spiked to 72 MB anyway because the limit was a soft target, not
an actual cap.

I then overcorrected to 64 MiB. Then, when the actual peak turned out
to be only ~18 MB, I tried to lower it back to 32 MiB.

A key insight came from a design review:

> **"Shouldn't we prepare for panic-tuning always? That's the whole
> point, right?"**

That changed my thinking. For a normal application, memory ceilings are
mostly about preventing waste. For a **monitoring tool**, the ceiling
exists specifically for the moment when everything else is going wrong.
The price of a generous limit is zero bytes when the tool is idle — the
runtime never pre-allocates unused memory. The benefit of a generous
limit is resilience during the exact minute when the operator is relying
on the dashboard to tell them what's happening.

So the final rule is:

- **Typical RSS: ~9 MB** (well under the 15 MB goal)
- **Peak during heavy sync: ~14 MB**
- **Ceiling: 64 MiB** (plenty of runway for the buffer at capacity plus
  a parse burst plus anything unforeseen)
- **Result: lightweight when idle, rock-solid when stressed**

Same principle applies to most resilience tuning: you don't size your
fire exits for an average Tuesday. You size them for the worst day
anyone could imagine, then forget about them on the 999 normal days.

---

## What I'd tell my past self

If I were starting this project again, these are the things I'd
internalize from day one:

1. **Map the authoritative sources before writing any code.** Spend a
   day reading about journalctl, syslog, nginx's log format, the kernel
   log, and how systemd exposes everything. The design falls out of
   understanding what's already there.

2. **Write the "bad day" scenarios first.** What does this tool look
   like when the monitored server is under a DDoS? When the disk is full?
   When the monitoring tool itself was just restarted? Design for those,
   and the happy path handles itself.

3. **A dashboard is a lens, not a database.** Build it to make existing
   truth visible, not to become the new source of truth.

4. **Labels are a feature.** Telling the user "this is our interpretation"
   versus "this is what the OS said" is worth more than any graph.

5. **Ceilings are not targets.** A 64 MiB ceiling doesn't mean "use 64
   MiB" — it means "you have 64 MiB to work with if things go sideways."
   The tool still runs at 9 MB on a normal day.

---

## Comparison to other tools in the space

For context, here's where asmt sits relative to other monitoring tools on
the same job:

| Tool | Typical RSS |
|---|---|
| **asmt (this project)** | **~9 MB steady, ~14 MB peak** |
| node_exporter (Prometheus) | ~15–30 MB |
| collectd (C, no web UI) | ~5–15 MB |
| Zabbix agent | ~10–20 MB |
| telegraf | ~30–80 MB |
| Glances (Python) | ~20–50 MB |
| Datadog agent | ~80–150 MB |
| Netdata | ~50–200 MB |
| New Relic infrastructure agent | ~50–100 MB |

For a tool that includes a built-in web dashboard, metrics history, log
aggregation, journal sync, and alerting, being lighter than almost every
alternative is a direct consequence of the design choices above. Aggregation
instead of accumulation, streaming instead of buffering, and bounded
subprocesses instead of unbounded work queues.

---

## Closing thought

The single most valuable lesson from this project wasn't technical. It was
philosophical:

> **A good monitoring tool earns its existence by making existing truth
> easier to see, not by claiming to be a new kind of truth.**

Linux already keeps excellent records. Operators don't have a data problem
— they have a *findability* problem. Solving findability in a lightweight,
honest, resilient way is a meaningful contribution. Trying to replace the
underlying systems was the wrong instinct, and catching that instinct
early (by listening to good design critique) was the most important
course-correction of the project.
