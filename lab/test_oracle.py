"""Unit tests for lab/oracle.py, an incident's truth from what the oracle
saw (docs/M6_PLAN.md, 4.3). No QEMU, no guest: the observations are made
up, one per outcome class and per rule.

  python3 -m unittest discover -s lab -p 'test_oracle.py'
"""
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)
sys.dont_write_bytecode = True

import oracle  # noqa: E402
from oracle import INCONCLUSIVE, Seen  # noqa: E402

FSTAB = {"id": "fstab-x", "path": "/etc/fstab"}
GRUBCFG = {"id": "grub-x", "path": "/boot/grub/grub.cfg"}
HARMLESS = {"id": "fstab-y", "path": "/etc/fstab", "harmless": True}


def effect(**kw):
    return Seen("effect", **kw)


def boot(**kw):
    return Seen("boot", **kw)


class TestClassify(unittest.TestCase):
    def c(self, seen, grub_file=False):
        return oracle.classify(seen, grub_file)[0]

    def test_one_observation_per_class(self):
        sure = {"causal": True, "runs": 2}
        for seen, want in (
                (boot(end="no-boot", **sure), "no-boot"),
                (boot(end="emergency", **sure), "emergency"),
                (effect(sudo="refused", causal=True), "access-lost"),
                (effect(console=False, causal=True), "access-lost"),
                (effect(admin=False, causal=True), "access-lost"),
                (effect(network=False, causal=True), "access-lost"),
                (effect(ssh=False, **sure), "access-lost"),
                (effect(applied=1), "rejected"),
                (effect(unit="backup.service", failed=("backup.service",)), "rejected"),
                (effect(failed=("systemd-sysctl.service",)), "degraded"),
                (effect(exposure=(False, True)), "exposed"),
                (effect(delayed=True), "delayed"),
                (effect(expect=(True, False)), "changed"),
                (effect(sudo="warning"), "changed"),
                (effect(messages=("udev: Invalid key",)), "changed"),
                (effect(expect=(True,), exposure=(False,)), "same")):
            self.assertEqual(self.c(seen), want, seen)

    def test_the_worse_sign_names_the_class(self):
        # Emergency is read from the console: ssh up at the end does not hide it.
        self.assertEqual(self.c(boot(end="emergency", causal=True, runs=2, ssh=True)), "emergency")
        self.assertEqual(self.c(effect(applied=1, failed=("x.service",), expect=(False,))), "rejected")
        self.assertEqual(self.c(effect(failed=("x.service",), exposure=(True,))), "degraded")
        # The restarted unit's own failure is the rejection; another unit is degraded.
        c, why = oracle.classify(effect(unit="a.service", failed=("a.service", "b.service")))
        self.assertEqual((c, why), ("rejected", ["a.service failed"]))

    def test_flakes_are_never_a_label(self):
        for end in oracle.FLAKES:
            self.assertEqual(self.c(boot(end=end, causal=True, runs=2)), INCONCLUSIVE, end)
        with self.assertRaises(ValueError):
            self.c(boot(end="halted"))

    def test_grub_prompt_only_for_a_file_grub_reads(self):
        self.assertEqual(self.c(boot(end="grub-prompt", causal=True, runs=2)), INCONCLUSIVE)
        self.assertEqual(self.c(boot(end="grub-prompt", causal=True, runs=2), grub_file=True), "no-boot")
        self.assertEqual(self.c(boot(end="grub-prompt", causal=False, runs=2), grub_file=True), INCONCLUSIVE)

    def test_a_lockout_needs_its_cause_and_two_runs(self):
        for end in ("no-boot", "emergency"):
            self.assertEqual(self.c(boot(end=end, causal=False, runs=2)), INCONCLUSIVE)
            self.assertEqual(self.c(boot(end=end, causal=True, runs=1)), INCONCLUSIVE)
        self.assertEqual(self.c(effect(sudo="refused")), INCONCLUSIVE)
        # Only ssh lost: two runs out of two as well.
        self.assertEqual(self.c(effect(ssh=False, causal=True)), INCONCLUSIVE)
        # Lost with another sign too: one run is enough.
        self.assertEqual(self.c(effect(ssh=False, console=False, causal=True)), "access-lost")


class TestLabel(unittest.TestCase):
    def test_worst_wins_and_when(self):
        # As U03 in the plan: same when it takes effect, lost after the reboot.
        lab = oracle.label(FSTAB, [effect(), boot(ssh=False, console=False, causal=True)])
        self.assertEqual(lab[:3], ("access-lost", "boot", "blocker"))
        # As H05: lost when it takes effect; the boot shows the same.
        lab = oracle.label(FSTAB, [effect(ssh=False, console=False, causal=True),
                                   boot(ssh=False, console=False, causal=True)])
        self.assertEqual(lab[:3], ("access-lost", "effect", "blocker"))
        lab = oracle.label(FSTAB, [effect(expect=(False,)), boot(end="emergency", causal=True, runs=2)])
        self.assertEqual(lab[:3], ("emergency", "boot", "blocker"))
        lab = oracle.label(FSTAB, [boot(expect=(False,)), effect(expect=(False,))])
        self.assertEqual(lab[:3], ("changed", "effect", "warning"))
        self.assertEqual(oracle.label(FSTAB, [effect(), boot()])[:3], ("same", "effect", "none"))
        # Two warnings: the more specific class, delayed, names the label.
        lab = oracle.label(FSTAB, [effect(expect=(False,)), boot(delayed=True)])
        self.assertEqual(lab[:3], ("delayed", "boot", "warning"))

    def test_harmless_is_observed_once(self):
        self.assertEqual(oracle.label(HARMLESS, [effect()])[:3], ("same", "effect", "none"))
        self.assertEqual(oracle.label(HARMLESS, [effect(), boot()]).outcome, INCONCLUSIVE)
        self.assertEqual(oracle.label(FSTAB, [effect()]).outcome, INCONCLUSIVE)
        self.assertEqual(oracle.label(FSTAB, []).outcome, INCONCLUSIVE)

    def test_inconclusive(self):
        lab = oracle.label(FSTAB, [effect(), boot()], control_ok=False)
        self.assertEqual(lab, (INCONCLUSIVE, None, None, ["the control boot failed"]))
        lab = oracle.label(FSTAB, [effect(failed=("x.service",)), boot(end="slow-udev")])
        self.assertEqual(lab[:2], (INCONCLUSIVE, "boot"))
        self.assertIn("slow-udev", lab.reasons[0])

    def test_grub_files(self):
        sure = boot(end="grub-prompt", causal=True, runs=2)
        self.assertEqual(oracle.label(GRUBCFG, [effect(), sure]).outcome, "no-boot")
        self.assertEqual(oracle.label({"id": "c", "path": "/etc/grub.d/40_custom"}, [effect(), sure]).outcome,
                         "no-boot")
        self.assertEqual(oracle.label(FSTAB, [effect(), sure]).outcome, INCONCLUSIVE)

    def test_classes_and_severities(self):
        self.assertEqual([c for c, _ in oracle.CLASSES][-1], "same")
        self.assertEqual({s for _, s in oracle.CLASSES}, {"blocker", "error", "warning", "none"})
        # Plan 4.3's table, with question 6's recommendations.
        for c, s in (("no-boot", "blocker"), ("emergency", "blocker"), ("access-lost", "blocker"),
                     ("degraded", "error"), ("rejected", "error"), ("exposed", "error"),
                     ("delayed", "warning"), ("changed", "warning"), ("same", "none")):
            self.assertEqual(oracle.SEVERITY[c], s)


class TestLabels(unittest.TestCase):
    SHA, REV = "6a" * 32, "1"

    def setUp(self):
        self.incs = [dict(id="fstab-x", path="/etc/fstab", change=[{"append": "x"}], applies="fstab-now",
                          cause="c", fix="restore", guess="blocker"),
                     dict(id="hosts-y", path="/etc/hosts", change=[{"append": "y"}], applies="now",
                          cause="c", fix="restore", guess="warning")]
        self.labels = oracle.new_labels(self.SHA, self.REV)
        for inc in self.incs:
            self.labels["labels"][inc["id"]] = oracle.record(inc, oracle.Label("same", "effect", "none", []),
                                                             ["runs/x/%s.log" % inc["id"]])

    def stale(self, incs=None, **kw):
        return oracle.stale(self.labels, self.incs if incs is None else incs,
                            kw.get("sha", self.SHA), kw.get("rev", self.REV), kw.get("profile", oracle.PROFILE))

    def test_current_labels_are_not_stale(self):
        self.assertEqual(self.stale(), {})
        # Prose does not make a label stale.
        self.assertEqual(self.stale([dict(self.incs[0], cause="Other words.", guess="error"), self.incs[1]]), {})

    def test_what_makes_a_label_stale(self):
        changed = [dict(self.incs[0], change=[{"append": "z"}]), self.incs[1]]
        self.assertEqual(self.stale(changed), {"fstab-x": "the incident changed since its label"})
        new = self.incs + [dict(self.incs[1], id="hosts-z")]
        self.assertEqual(self.stale(new), {"hosts-z": "no label"})
        self.assertEqual(self.stale(self.incs[:1]), {"hosts-y": "a label for an incident no longer in the set"})
        for kw in ({"sha": "7b" * 32}, {"rev": "2"}, {"profile": "installed-server-2"}):
            got = self.stale(**kw)
            self.assertEqual(set(got), {"fstab-x", "hosts-y"}, kw)
            self.assertTrue(all(v.startswith("labelled on another guest") for v in got.values()), got)
        self.labels["labels"]["hosts-y"] = oracle.record(
            self.incs[1], oracle.Label(oracle.INCONCLUSIVE, "boot", None, ["the lab's own stall, never a label"]))
        self.assertEqual(self.stale(), {"hosts-y": "inconclusive: the lab's own stall, never a label"})

    def test_write_and_read(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "labels.json")
            oracle.write_labels(path, self.labels)
            self.assertEqual(oracle.read_labels(path), self.labels)
            self.assertEqual(os.listdir(d), ["labels.json"])
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o644)
            rec = self.labels["labels"]["fstab-x"]
            self.assertEqual(sorted(rec), ["evidence", "hash", "outcome", "reasons", "severity", "when"])


if __name__ == "__main__":
    unittest.main()
