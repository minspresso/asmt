# LEARNINGS

The design journey of **asmt** — a lightweight Linux server monitoring
dashboard. Written in plain language for leadership, recruiters, and anyone
curious about the engineering decisions behind it.

---

## The goal

A monitoring dashboard for a **single server** that is:

- **Trivial to install** — one command, auto-detects everything.
- **Tiny** — under 15 MB of memory in normal use, never more than 50 MB.
- **Honest** — shows what's actually happening, never invents data.
- **Fast for humans** — answers "what went wrong and when?" in seconds.

I picked **Go** because it compiles to a single static binary, starts in
milliseconds, and gives tight memory control. Final result: **~9 MB steady,
~14 MB peak** — lighter than almost every alternative on the market.

---

## The problem worth solving

Linux already has world-class logging. nginx, PHP, MySQL, the kernel, and
systemd all keep their own durable, timestamped records. The data is there.

The problem is **findability**. When something breaks, an operator has to
know where five different logs live, how to read each format, and how to
correlate them by time. That's a skill that takes years to build, and it
burns precious incident-response minutes on every outage.

**asmt's value is time saved during incidents.** It knows where every
source of truth lives and assembles them into one timeline anyone can read.

---

## The biggest design mistake (and the fix)

I started with the wrong instinct: **make our tool the source of truth.**
Every check would write to our internal database; the dashboard would read
from it. One pipeline, one display, simple.

It was wrong for three reasons:

1. **Our buffer is only as good as our uptime.** Crash, restart, or even a
   brief lockup could lose data. The OS journal, by design, doesn't.
2. **We were duplicating something the OS already does well.**
3. **Our cache was masquerading as truth.** A bug in our recording would
   silently make the dashboard wrong, and there'd be no way to spot it.

To paper over the gaps, I almost shipped something even worse: **synthetic
log entries** that filled missing days with "average" placeholders. That's
not a cache mistake — that's lying to the user with made-up data.

The right model came next:

> **Our tool is a dashboard on top of existing truth, not a replacement
> for it. The OS owns the data. We just make it easier to see.**

In practice this meant:

- The OS journal and software logs are always authoritative.
- Our buffer is a cache. If it loses data, the OS still has the original.
- Missing data is shown as missing — never as a fabricated entry.
- Our own observations are explicitly labeled as such, distinct from
  records pulled from the journal.

This shift changed the entire mental model of the project. The tool stopped
trying to *be* a log system and became a **smart lens** on the logs that
already exist.

---

## Designing for the worst day, not the average one

Every engineer's first instinct is to optimize the happy path. But a
**monitoring tool's whole job is to work hardest exactly when everything
else is failing.** That demands a different mindset.

**The crisis scenario:** A server under attack producing 500 error logs per
second is generating **43 million entries per day**. No naive list can hold
that. We needed something smarter.

### The aggregation insight (a receipt analogy)

Imagine a busy bakery selling 500 identical loaves between 9:00 and 9:15.
Two ways to record it:

- **Print 500 receipts.** End of day: 200,000 slips. You need a warehouse.
- **Print one receipt** that says "500 × bread, 9:00-9:15." Same information,
  one piece of paper. You lose the per-customer breakdown, but that's not
  what anyone needs to know.

asmt uses the second approach. **One entry per (15-minute window × error
type),** with a counter. The result:

| Server load | Raw events/day | Stored entries | Memory |
|---|---|---|---|
| Quiet | ~100 | ~20 | 15 KB |
| Busy | ~10,000 | ~500 | 350 KB |
| Under attack | 43,000,000 | ~2,000 | 1.4 MB |

**A server under attack uses essentially the same memory as a quiet one.**
Only the counter goes up.

### Verification

I load-tested the design at the advertised target (500 events/sec for 24
hours). Result: **2 million events/sec throughput**, no measurable memory
growth, buffer stable at ~2,000 entries. The tool handles roughly **4,000×
the design target** before becoming the bottleneck.

---

## The memory ceiling lesson

I tuned the memory ceiling aggressively at first — 10 MB, then 12 MB.
"Smaller is better, right?" Then I ran a stress test and it thrashed,
spiking to 72 MB anyway because the limit was a soft hint, not a hard cap.

I overcorrected to 64 MB. When the actual peak settled at 14 MB, I tried to
lower it back to 32 MB. A design review caught me:

> **"Shouldn't we prepare for the worst day always? That's the whole point."**

That reframing changed everything. For a normal app, the memory ceiling is
about preventing waste. For a **monitoring tool**, the ceiling exists for
the moment when everything else is failing — and that's exactly when you
need headroom. A generous limit costs **zero RSS** when idle (the runtime
doesn't pre-allocate). It only matters during a crisis.

Final settings: **9 MB typical, 14 MB peak, 64 MB ceiling.** The same
principle applies anywhere: **size your fire exits for the worst day, not
the average Tuesday.**

---

## Security: locked doors before opening the building

Before publishing as open source, I commissioned a thorough security audit.
Eighteen findings came back — none critical, but worth fixing before any
stranger downloaded the tool. The philosophy is the same as the memory work:

> **A monitoring tool has to work hardest when everything else is failing.
> That's also when an attacker is most likely to be probing. The defenses
> have to assume the worst day, not the average one.**

The fixes fell into a few categories:

- **Lock the front door by default.** The dashboard listens only to the
  local machine. If an operator changes that setting, the tool now logs a
  loud warning so nobody accidentally exposes it without authentication.
- **Distrust everything from outside.** Every value from a web request is
  strictly validated before it touches a system command, file, or screen.
- **Don't tell strangers your secrets.** Internal error details (paths,
  subprocess output, DB strings) stay in the server's log; clients only
  see short safe labels like "sync failed".
- **Block drive-by attacks.** A malicious website in another browser tab
  can no longer trick the dashboard into running operations on the user's
  behalf. (Industry term: CSRF protection.)
- **Data files as private mail.** History and log files are readable only
  by the owner, not every user on the machine.
- **Verify what you download.** The one-line installer now checks the
  cryptographic fingerprint of the binary before installing it.
- **Audit on every change.** A continuous-integration pipeline runs the
  build, tests, a vulnerability scanner, and a linter on every commit.

The deeper lesson: **security isn't a feature you bolt on at the end.**
It's the same discipline as bounded memory and graceful failure handling —
assume the worst, write the test for it, make the safe behavior the
default. All 18 fixes took about an hour. None changed how the tool feels
to a normal user.

---

## The auth lesson: don't make the tool depend on the thing it monitors

The first real deployment of asmt put `/stats.html` behind WordPress's
existing admin session. The reasoning sounded clean: the operator was
already logged in, so reuse that session — single sign-on, no extra
password, no separate user database.

It failed in two ways that only became visible during real incidents:

**1. The dashboard became unreachable exactly when it was needed.**
A traffic spike that saturated PHP-FPM also broke the auth check, because
the auth check went *through* PHP. The monitoring tool was now coupled
to the health of the thing it was supposed to be monitoring. The
worst-day principle applies to authentication too: if your auth path can
fail under load, your dashboard fails under load.

**2. The WAF flagged the host application's session cookie as a scanner
token.** WordPress session cookies are 200+ bytes of high-entropy hex —
exactly what cloud WAF scanner-detection rules are tuned to catch. The
day a stricter WAF rule went live, every logged-in admin started seeing
404s on the dashboard URL while anonymous visitors saw normal pages.
Hours of debugging later, the cause was "your cookie looks like an
attack signature."

The fix in both cases was the same: **decouple.** Replace the
auth-via-host-app with HTTP Basic Auth at the reverse proxy. Two lines
of nginx config, one flat password file, zero PHP, zero database, zero
cookie. Now the dashboard works during a host-app outage, works with no
host app at all, and is invisible to WAF cookie heuristics because Basic
Auth headers are short and structured.

> **A monitoring tool's auth should depend on as few things as possible
> — ideally only the kernel and the reverse proxy already in front of
> it. Each extra component in the auth chain is one more thing that can
> fail exactly when you most need the dashboard to work.**

---

## Design principles (the rules I'd ship today)

1. **Trust the OS more than yourself.** The journal is always authoritative.
2. **Never invent data.** Missing is information; faking is a lie.
3. **Label everything.** "Observation" vs "OS record" matters.
4. **Validate every input.** Especially before passing to a subprocess.
5. **Bound every subprocess.** Time, output size, line count — all capped.
6. **Stream, don't accumulate.** Process one item, throw it away, repeat.
7. **Aggregate on write, not on read.** Memory bounded by dimensions, not
   by event rate.
8. **Single-flight expensive operations.** Don't let busy users DoS
   themselves.
9. **Optimize the ceiling for the worst case, usage for the typical case.**
10. **Degrade gracefully.** No feature should require the perfect
    environment.
11. **Supplement, don't replace.** The meta-rule that contains all the
    others.
12. **Minimize the auth dependency footprint.** A monitoring tool's auth
    path should not require the host application or its database to be
    healthy.

---

## How asmt compares

For a tool that includes a built-in dashboard, metrics history, log
aggregation, journal sync, and alerting:

| Tool | Typical RSS |
|---|---|
| **asmt (this project)** | **~9 MB steady, ~14 MB peak** |
| collectd (C, no UI) | ~5–15 MB |
| node_exporter (Prometheus) | ~15–30 MB |
| Zabbix agent | ~10–20 MB |
| Glances (Python) | ~20–50 MB |
| telegraf | ~30–80 MB |
| New Relic agent | ~50–100 MB |
| Datadog agent | ~80–150 MB |
| Netdata | ~50–200 MB |

Being lighter than almost every alternative is a direct consequence of the
design choices above: aggregation instead of accumulation, streaming instead
of buffering, bounded subprocesses instead of unbounded queues.

---

## Closing thought

The single most valuable lesson from this project wasn't technical:

> **A good monitoring tool earns its existence by making existing truth
> easier to see, not by claiming to be a new kind of truth.**

Linux already keeps excellent records. Operators don't have a data
problem — they have a **findability problem**. Solving findability in a
lightweight, honest, resilient way is a meaningful contribution. Catching
the wrong instinct early (by listening to good design critique instead of
defending the original idea) was the most important course-correction of
the entire project.
