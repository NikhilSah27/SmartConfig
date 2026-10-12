#!/usr/bin/env python3
"""oracle.py: an incident's truth from what the oracle saw
(docs/M6_PLAN.md, 4.3).

The oracle looks at the guest after a change takes effect, and again
after a reboot (every incident that is not harmless). This module turns
what it saw into the label: the outcome class, when it showed, and the
severity that class gives. What it saw comes in as Seen records;
gathering them through the root channel and from the console is steps
4 to 6.

  CLASSES   the outcome classes, worst first, with the severity of each
  FLAKES    the lab's own retry signatures: never a label
  classify  the class one observation shows, with its reasons
  label     an incident's label from its observations

Python 3 standard library only.
"""
import collections
import dataclasses
import fnmatch

# Worst first. Within one severity the order only says which name a
# label takes when an observation shows two.
CLASSES = (("no-boot", "blocker"), ("emergency", "blocker"), ("access-lost", "blocker"),
           ("rejected", "error"), ("degraded", "error"), ("exposed", "error"),
           ("delayed", "warning"), ("changed", "warning"), ("same", "none"))
SEVERITY = dict(CLASSES)
RANK = {c: i for i, (c, _) in enumerate(CLASSES)}
INCONCLUSIVE = "inconclusive"

# Where a boot ends, as the boot oracle (step 6) reads the console:
# "emergency" is emergency or rescue mode; "no-boot" a GRUB error, a
# panic or an initramfs shell.
ENDS = ("up", "no-boot", "emergency", "grub-prompt")
# The lab retries on these (console.py, e2e.py): a stall, TCG's own panic,
# a healthy boot TCG starved into emergency mode, a login prompt with no
# ssh, the VGA gap, a missed menu. None of them is ever a label.
FLAKES = ("stall", "tcg-panic", "slow-udev", "no-ssh", "vga-gap", "missed-menu")
# Where "grub>" can be the real outcome.
GRUB_FILES = ("/boot/grub/grub.cfg", "/boot/grub/custom.cfg", "/etc/grub.d/*")

Label = collections.namedtuple("Label", "outcome when severity reasons")


@dataclasses.dataclass(frozen=True)
class Seen:
    """What the oracle saw once: after the change took effect
    (phase "effect") or after the reboot (phase "boot")."""
    phase: str
    sudo: str = "works"       # sudo -S -v as the admin: works, refused or warning
    ssh: bool = True          # a new ssh login (key) from the host
    console: bool = True      # a console login (password)
    admin: bool = True        # id <admin> knows the admin
    network: bool = True      # a route to the outside, through the root channel
    applied: int = 0          # the applies command's exit status (update-grub's among them)
    unit: str = ""            # the unit a restart:UNIT applies restarted
    failed: tuple = ()        # units failed now that did not fail in the control's list
    expect: tuple = ()        # each expect probe: passed or not
    exposure: tuple = ()      # each exposure probe: passed (exposed) or not
    messages: tuple = ()      # the consumers' journal messages the control does not have
    delayed: bool = False     # the boot or a login waited past its budget, then went on
    end: str = "up"           # where the boot ended (boot only): ENDS or FLAKES
    causal: bool = False      # a line names the incident's cause (a device timeout with its
                              # UUID, the initramfs's missing root, a GRUB error, the
                              # consumer's own refusal)
    runs: int = 1             # of 2 runs, how many saw this same outcome


def classify(seen, grub_file=False):
    """(class, reasons) for one observation, or (INCONCLUSIVE, reasons).
    grub_file: the incident changes a file GRUB reads at boot, where a
    grub> prompt can be the real outcome."""
    if seen.end in FLAKES:
        return INCONCLUSIVE, ["the lab's own %s, never a label" % seen.end]
    if seen.end not in ENDS:
        raise ValueError("unknown boot end %r" % seen.end)
    if seen.end in ("no-boot", "emergency", "grub-prompt"):
        if seen.end == "grub-prompt" and not grub_file:
            return INCONCLUSIVE, ["a grub> prompt, and the incident changes no file GRUB reads"]
        why = []
        if not seen.causal:
            why.append("no line names the cause")
        if seen.runs < 2:
            why.append("seen in %d run of 2" % seen.runs)
        if why:
            return INCONCLUSIVE, ["%s: %s" % (seen.end, "; ".join(why))]
        return ("emergency" if seen.end == "emergency" else "no-boot"), [seen.end]
    lost = [what for what, ok in (("sudo refused", seen.sudo != "refused"), ("no ssh login", seen.ssh),
                                  ("no console login", seen.console), ("the admin unknown", seen.admin),
                                  ("no network", seen.network)) if not ok]
    if lost:
        why = []
        if not seen.causal:
            why.append("no line names the cause")
        if lost == ["no ssh login"] and seen.runs < 2:
            why.append("ssh is the only sign, seen in %d run of 2" % seen.runs)
        if why:
            return INCONCLUSIVE, ["access-lost (%s): %s" % (", ".join(lost), "; ".join(why))]
        return "access-lost", lost
    if seen.applied != 0 or (seen.unit and seen.unit in seen.failed):
        why = ["the applies command exited %d" % seen.applied] if seen.applied else []
        if seen.unit and seen.unit in seen.failed:
            why.append("%s failed" % seen.unit)
        return "rejected", why
    others = [u for u in seen.failed if u != seen.unit]
    if others:
        return "degraded", ["newly failed: %s" % ", ".join(sorted(others))]
    if any(seen.exposure):
        return "exposed", ["exposure probe %d passed" % i for i, p in enumerate(seen.exposure) if p]
    if seen.delayed:
        return "delayed", ["waited past its budget"]
    changed = ["expect probe %d failed" % i for i, p in enumerate(seen.expect) if not p]
    if seen.sudo == "warning":
        changed.append("sudo warns")
    changed += ["new message: %s" % m for m in seen.messages]
    if changed:
        return "changed", changed
    return "same", []


def label(inc, seens, control_ok=True):
    """The Label of incident INC from its observations SEENS: the worst
    class wins, and "when" is the phase it showed in (effect first on a
    tie). A harmless incident is observed after it takes effect only;
    any other needs both phases. A failed control boot, a missing phase
    or an inconclusive observation makes the label inconclusive."""
    if not control_ok:
        return Label(INCONCLUSIVE, None, None, ["the control boot failed"])
    need = ("effect",) if inc.get("harmless") else ("effect", "boot")
    phases = [s.phase for s in seens]
    if sorted(phases) != sorted(need):
        return Label(INCONCLUSIVE, None, None, ["observed %s, needs %s" % (", ".join(phases) or "nothing",
                                                                           ", ".join(need))])
    grub_file = any(fnmatch.fnmatchcase(inc["path"], g) for g in GRUB_FILES)
    results = []
    for s in sorted(seens, key=lambda s: need.index(s.phase)):
        c, why = classify(s, grub_file)
        if c == INCONCLUSIVE:
            return Label(INCONCLUSIVE, s.phase, None, why)
        results.append((RANK[c], s.phase, c, why))
    _, when, c, why = min(results, key=lambda r: r[0])
    return Label(c, when, SEVERITY[c], why)
