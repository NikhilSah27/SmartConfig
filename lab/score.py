#!/usr/bin/env python3
"""score.py: M6's score, the measures of docs/M6_PLAN.md, 6.2.

It compares what sc did on each incident (the sc-side run, step 8) with
the incident's truth (eval/labels.json, oracle.py), and writes the
report. Each surface is compared by the findings the change added.

What the sc-side run gives, one record per incident:

  {"id": "fstab-uuid-one-char", "changed_lines": [4],
   "surfaces": {
     "edit":    {"decision": "refused", "added": [FINDING, ...]},
     "scd":     {"recorded": true, "added": [FINDING, ...]},
     "check":   {"added": [FINDING, ...]},
     "restore": {"healthy": true, "asked": false}}}

  FINDING: {"rule": "fstab-source-missing", "severity": "blocker",
            "line": 4}; "line" is null for a finding with none.
  decision: "saved", "refused" (an added blocker or error, at end of
            input) or "unchanged".
  A surface the incident is not scored on (incidents.surfaces) is left
  out. "asked": sc restore asked before it wrote (and the harness then
  restored with --force).

  score     the measures, as data
  report    the measures as Markdown, for eval/REPORT.md

Python 3 standard library only.
"""
import oracle

RANK = {"none": 0, "warning": 1, "error": 2, "blocker": 3}
TRUTHS = ("blocker", "error", "warning")
FINDING_SURFACES = ("edit", "scd", "check")


def highest(added):
    """The highest severity among the added findings, or "none"."""
    return max((f["severity"] for f in added), key=RANK.__getitem__, default="none")


def score(incs, labels, results):
    """The measures of plan 6.2 over the labelled incidents. INCS: the
    set (incidents.load_all); LABELS: labels.json; RESULTS: the sc-side
    run's records. An incident with no usable label is not scored: it is
    listed under "unlabelled" with the reason (oracle.stale), and so is a
    labelled one with no result."""
    by_id = {r["id"]: r for r in results}
    unusable = oracle.stale(labels, incs, labels.get("image_sha256"), labels.get("provision_rev"),
                            labels.get("profile"))
    s = {"catch": {}, "agreement": {}, "lines": {}, "false_alarms": {}, "disagree": [], "restore": {},
         "misses": [], "unlabelled": {}, "scored": 0, "harmless": 0,
         "guest": {k: labels.get(k) for k in ("image_sha256", "provision_rev", "profile")}}
    for surface in FINDING_SURFACES:
        s["catch"][surface] = {t: {"n": 0, "at_or_above": 0, "any": 0} for t in TRUTHS}
        s["agreement"][surface] = {"right": 0, "over": 0, "under": 0, "missed": 0}
        s["lines"][surface] = {"caught": 0, "named": 0}
        s["false_alarms"][surface] = {"warning": 0, "error": 0, "blocker": 0}
    restore = s["restore"] = {"n": 0, "healthy": 0, "asked": [], "unhealthy": []}
    for inc in incs:
        i = inc["id"]
        if i in unusable:
            s["unlabelled"][i] = unusable[i]
            continue
        r = by_id.get(i)
        if r is None:
            s["unlabelled"][i] = "no result from the sc-side run"
            continue
        truth = labels["labels"][i]
        sev = truth["severity"]
        s["scored"] += 1
        surf = r["surfaces"]
        got = {k: highest(surf[k]["added"]) for k in FINDING_SURFACES if k in surf}
        if inc.get("harmless"):
            s["harmless"] += 1
            for k, h in got.items():
                if h != "none":
                    s["false_alarms"][k][h] += 1
        else:
            missed = []
            for k, h in got.items():
                c = s["catch"][k][sev]
                c["n"] += 1
                c["any"] += h != "none"
                c["at_or_above"] += RANK[h] >= RANK[sev]
                a = s["agreement"][k]
                if h == "none":
                    a["missed"] += 1
                    missed.append(k)
                else:
                    a["right" if h == sev else "over" if RANK[h] > RANK[sev] else "under"] += 1
                    ln = s["lines"][k]
                    ln["caught"] += 1
                    changed = set(r.get("changed_lines", ()))
                    ln["named"] += any(f.get("line") in changed for f in surf[k]["added"])
            if missed:
                s["misses"].append({"id": i, "surfaces": missed, "cause": inc["cause"],
                                    "outcome": truth["outcome"], "when": truth["when"], "severity": sev,
                                    "evidence": truth["evidence"]})
        if len(set(got.values())) > 1:
            s["disagree"].append({"id": i, "highest": dict(sorted(got.items()))})
        if "restore" in surf:
            restore["n"] += 1
            restore["healthy"] += bool(surf["restore"]["healthy"])
            if surf["restore"].get("asked"):
                restore["asked"].append(i)
            if not surf["restore"]["healthy"]:
                restore["unhealthy"].append(i)
    # Goal 4: no harmless incident gets an added finding at error or blocker.
    s["goal4"] = all(fa["error"] == 0 and fa["blocker"] == 0 for fa in s["false_alarms"].values())
    return s


def _pct(a, n):
    return "%d of %d" % (a, n) if n else "-"


def report(s):
    """The measures of SCORE as Markdown: one page, misses by name."""
    out = ["# How SmartConfig does on M6's incidents", ""]
    g = s["guest"]
    out += ["Measured on the lab guest: image sha256 `%s`, recipe %s, profile `%s`." % (
        (g["image_sha256"] or "?")[:12], g["provision_rev"], g["profile"]),
        "Scored: %d incidents (%d harmless); not scored: %d (listed at the end)." % (
            s["scored"], s["harmless"], len(s["unlabelled"])), ""]
    out += ["## Catch rate", "", "Of the incidents whose truth is at each severity, how many each surface "
            "flagged at that severity or higher, and at all.", "",
            "| Surface | blocker | error | warning |", "|---|---|---|---|"]
    for k in FINDING_SURFACES:
        cells = ["%s (any: %s)" % (_pct(c["at_or_above"], c["n"]), _pct(c["any"], c["n"]))
                 for c in (s["catch"][k][t] for t in TRUTHS)]
        out.append("| %s | %s |" % (k, " | ".join(cells)))
    out += ["", "## Severity", "", "| Surface | right | too high | too low | missed |", "|---|---|---|---|---|"]
    for k in FINDING_SURFACES:
        a = s["agreement"][k]
        out.append("| %s | %d | %d | %d | %d |" % (k, a["right"], a["over"], a["under"], a["missed"]))
    out += ["", "## The changed line", "", "Of the incidents a surface caught, how many it caught with a "
            "finding on the line the change touched.", ""]
    for k in FINDING_SURFACES:
        ln = s["lines"][k]
        out.append("- %s: %s" % (k, _pct(ln["named"], ln["caught"])))
    out += ["", "## False alarms", "", "Harmless incidents with a finding the change added, by its severity. "
            "Goal 4 (none at error or blocker): **%s**." % ("holds" if s["goal4"] else "fails"), "",
            "| Surface | warning | error | blocker |", "|---|---|---|---|"]
    for k in FINDING_SURFACES:
        fa = s["false_alarms"][k]
        out.append("| %s | %d | %d | %d |" % (k, fa["warning"], fa["error"], fa["blocker"]))
    out += ["", "## Surfaces that disagree", ""]
    out += ["- %s: %s" % (d["id"], ", ".join("%s %s" % kv for kv in d["highest"].items()))
            for d in s["disagree"]] or ["None."]
    r = s["restore"]
    out += ["", "## Restore", "", "%s restores left a healthy guest." % _pct(r["healthy"], r["n"])]
    if r["unhealthy"]:
        out.append("Not healthy after the restore: %s." % ", ".join(r["unhealthy"]))
    if r["asked"]:
        out.append("sc restore asked first (then restored with --force): %s." % ", ".join(r["asked"]))
    out += ["", "## Misses", ""]
    out += ["- **%s** (%s, %s at %s; missed by %s): %s Evidence: %s." % (
        m["id"], m["severity"], m["outcome"], m["when"], ", ".join(m["surfaces"]), m["cause"],
        ", ".join(m["evidence"]) or "none") for m in s["misses"]] or ["None."]
    out += ["", "## Not scored", ""]
    out += ["- %s: %s" % kv for kv in sorted(s["unlabelled"].items())] or ["None."]
    return "\n".join(out) + "\n"
