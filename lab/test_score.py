"""Unit tests for lab/score.py, M6's score (docs/M6_PLAN.md, 6.2), on
canned truths and sc-side results.

  python3 -m unittest discover -s lab -p 'test_score.py'
"""
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)
sys.dont_write_bytecode = True

import oracle  # noqa: E402
import score  # noqa: E402


def inc(i, **kw):
    d = dict(id=i, path="/etc/fstab", change=[{"append": i}], applies="fstab-now", cause="Cause of %s." % i,
             fix="restore", guess="blocker")
    d.update(kw)
    return d


def f(sev, line=None, rule="r"):
    return {"rule": rule, "severity": sev, "line": line}


INCS = [inc("a"), inc("b"), inc("c"), inc("h", harmless=True, guess="none"), inc("d"), inc("e")]
TRUTH = {"a": ("emergency", "boot", "blocker"), "b": ("rejected", "effect", "error"),
         "c": ("changed", "effect", "warning"), "h": ("same", "effect", "none"),
         "e": ("degraded", "boot", "error")}
RESULTS = [
    {"id": "a", "changed_lines": [4], "surfaces": {
        "edit": {"decision": "refused", "added": [f("blocker", 4)]},
        "scd": {"recorded": True, "added": [f("blocker", 4)]},
        "check": {"added": [f("blocker", 4), f("warning", 1)]},
        "restore": {"healthy": True, "asked": False}}},
    {"id": "b", "changed_lines": [2], "surfaces": {
        "edit": {"decision": "saved", "added": [f("warning", 9)]},
        "scd": {"recorded": True, "added": []},
        "check": {"added": [f("error", 2)]},
        "restore": {"healthy": False, "asked": False}}},
    {"id": "c", "changed_lines": [1], "surfaces": {
        "check": {"added": [f("blocker", None)]},
        "restore": {"healthy": True, "asked": True}}},
    {"id": "h", "changed_lines": [3], "surfaces": {
        "edit": {"decision": "saved", "added": []},
        "scd": {"recorded": True, "added": []},
        "check": {"added": [f("warning", 3)]}}},
    {"id": "d", "changed_lines": [], "surfaces": {"check": {"added": []}}},
]


def labels():
    lab = oracle.new_labels("6a" * 32, "1")
    for i in INCS:
        if i["id"] in TRUTH:
            o, w, s = TRUTH[i["id"]]
            lab["labels"][i["id"]] = oracle.record(i, oracle.Label(o, w, s, []), ["runs/%s.log" % i["id"]])
    return lab


class TestScore(unittest.TestCase):
    def setUp(self):
        self.s = score.score(INCS, labels(), RESULTS)

    def test_highest(self):
        self.assertEqual(score.highest([f("warning"), f("blocker"), f("error")]), "blocker")
        self.assertEqual(score.highest([]), "none")

    def test_what_is_scored(self):
        self.assertEqual((self.s["scored"], self.s["harmless"]), (4, 1))
        self.assertEqual(self.s["unlabelled"], {"d": "no label", "e": "no result from the sc-side run"})

    def test_catch_rate(self):
        c = self.s["catch"]
        self.assertEqual(c["check"]["blocker"], {"n": 1, "at_or_above": 1, "any": 1})
        self.assertEqual(c["check"]["error"], {"n": 1, "at_or_above": 1, "any": 1})
        self.assertEqual(c["check"]["warning"], {"n": 1, "at_or_above": 1, "any": 1})
        self.assertEqual(c["edit"]["error"], {"n": 1, "at_or_above": 0, "any": 1})
        self.assertEqual(c["scd"]["error"], {"n": 1, "at_or_above": 0, "any": 0})
        self.assertEqual(c["edit"]["warning"], {"n": 0, "at_or_above": 0, "any": 0})

    def test_severity_agreement(self):
        self.assertEqual(self.s["agreement"]["check"], {"right": 2, "over": 1, "under": 0, "missed": 0})
        self.assertEqual(self.s["agreement"]["edit"], {"right": 1, "over": 0, "under": 1, "missed": 0})
        self.assertEqual(self.s["agreement"]["scd"], {"right": 1, "over": 0, "under": 0, "missed": 1})

    def test_the_changed_line(self):
        self.assertEqual(self.s["lines"]["check"], {"caught": 3, "named": 2})  # c's finding has no line
        self.assertEqual(self.s["lines"]["edit"], {"caught": 2, "named": 1})  # b's is on line 9, not 2

    def test_false_alarms_and_goal_4(self):
        self.assertEqual(self.s["false_alarms"]["check"], {"warning": 1, "error": 0, "blocker": 0})
        self.assertTrue(self.s["goal4"])
        bad = [dict(r) for r in RESULTS]
        bad[3] = {"id": "h", "surfaces": {"scd": {"recorded": True, "added": [f("error", 3)]}}}
        s = score.score(INCS, labels(), bad)
        self.assertFalse(s["goal4"])
        self.assertIn("Goal 4 (none at error or blocker): **fails**", score.report(s))

    def test_disagree_misses_restore(self):
        self.assertEqual(self.s["disagree"], [
            {"id": "b", "highest": {"check": "error", "edit": "warning", "scd": "none"}},
            {"id": "h", "highest": {"check": "warning", "edit": "none", "scd": "none"}}])
        self.assertEqual([(m["id"], m["surfaces"]) for m in self.s["misses"]], [("b", ["scd"])])
        self.assertEqual(self.s["misses"][0]["evidence"], ["runs/b.log"])
        self.assertEqual(self.s["restore"], {"n": 3, "healthy": 2, "asked": ["c"], "unhealthy": ["b"]})

    def test_stale_labels_are_not_scored(self):
        changed = [dict(i, change=[{"append": "other"}]) if i["id"] == "a" else i for i in INCS]
        s = score.score(changed, labels(), RESULTS)
        self.assertEqual(s["unlabelled"]["a"], "the incident changed since its label")
        self.assertEqual(s["scored"], 3)

    def test_report(self):
        r = score.report(self.s)
        for want in ("Scored: 4 incidents (1 harmless); not scored: 2",
                     "| check | 1 of 1 (any: 1 of 1) | 1 of 1 (any: 1 of 1) | 1 of 1 (any: 1 of 1) |",
                     "| scd | 1 | 0 | 0 | 1 |", "- check: 2 of 3", "Goal 4 (none at error or blocker): **holds**",
                     "- b: check error, edit warning, scd none", "2 of 3 restores left a healthy guest.",
                     "Not healthy after the restore: b.", "asked first (then restored with --force): c.",
                     "- **b** (error, rejected at effect; missed by scd): Cause of b. Evidence: runs/b.log.",
                     "- d: no label", "- e: no result from the sc-side run"):
            self.assertIn(want, r)


if __name__ == "__main__":
    unittest.main()
