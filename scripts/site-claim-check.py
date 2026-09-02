#!/usr/bin/env python3
"""Pin the sentences on the website that carry a SECURITY claim.

Containment claims mostly, plus the audit claim, which has the same property:
someone acts on it, so being wrong costs them something real.

WHY THIS EXISTS. Dejima's containment boundary is the ISLAND, which is per
PROJECT. An island holds several agents; they share it, each on its own git
worktree. "Every agent is walled off from the other agents" is therefore false,
and it is the error a reader notices fastest, because it is a security claim.

That false claim has been fixed on this site THREE TIMES BY HAND. Each time,
what found it was a person re-asking the question — never a check. And twice it
came back inside copy someone rewrote in good faith, because rewriting a
sentence about isolation is the easiest way in the world to reintroduce it.

WHY A PHRASE LIST WOULD NOT DO. The third instance said "each agent runs in a
container": the per-agent claim in full, containing none of "isolated", "walled
off" or "from each other" — the words every previous sweep searched for. A list
of banned phrases goes quiet exactly when a NEW wording appears, and whoever
writes the new wording is by construction not editing the list. So this pins
what the sentences SAY rather than scanning for what they must not say. Any
rewording of a pinned sentence fails until a human looks at it.

WHY NOT A GOLDEN OVER THE WHOLE PAGE. These are marketing pages, mostly prose.
A golden would fail on every legitimate copy edit and be switched off within a
week — that is not hypothetical, a description guard on this repo produced 29
false positives and was deleted rather than shipped. The scope here is the
claim-bearing sentences only. Everything else on the page stays free.

BEING BRITTLE HERE IS THE FEATURE. If you reworded one of these deliberately,
update the manifest in the same commit and say in the message why the new
wording is still true. That conversation is the entire point: this file exists
to make a security claim expensive to change by accident and cheap to change on
purpose.

Run from the repo root: python3 scripts/site-claim-check.py
"""

import io
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# text  : the claim-bearing fragment, pinned exactly.
# count : how many times it must appear. A count of 2 is not a typo — it means
#         the sentence is MIRRORED, usually between a JSON-LD block and the
#         visible copy of the same FAQ answer. That mirroring is how the claim
#         survived a fix once already: the visible copy was corrected and the
#         structured-data copy was not, and it stayed live for days. Pinning the
#         count is what makes a half-fix fail.
# why   : what makes this sentence TRUE. Printed on failure, so whoever trips
#         this can decide whether the new wording is still true instead of
#         restoring the old text to get green. A guard that teaches nothing gets
#         satisfied by reverting.
CLAIMS = [
    {
        "file": "llms.txt",
        "text": ("Each project runs sandboxed in its own container, called an island, isolated "
                 "from your machine and from your other projects. An island can hold several "
                 "agents; they share it, each on its own git worktree."),
        "count": 1,
        "why": ("Scoped to the PROJECT and then says outright that an island holds several "
                "agents sharing it. This file is written for models; it is the highest-leverage "
                "place on the site to be wrong, and it carried the per-agent claim for days."),
    },
    {
        "file": "index.html",
        "text": ("Each project is sandboxed on hardware you own — isolated from your machine and "
                 "your other projects"),
        "count": 1,
        "why": "meta description. Scoped to the project, not the agent.",
    },
    {
        "file": "index.html",
        "text": ("Each project sandboxed on hardware you own — isolated from your machine and from "
                 "your other projects"),
        "count": 1,
        "why": ("og:description. This one said 'and each other' and survived a sweep that searched "
                "for 'from each other' — the phrasings differ by two words."),
    },
    {
        "file": "index.html",
        "text": "Every project is walled off from your machine, your files, and your other projects",
        "count": 1,
        "why": "Visible security bullet. Said 'Every agent' until it was corrected.",
    },
    {
        "file": "index.html",
        "text": "│ per agent:  own git worktree · shares the island with its peers│",
        "count": 1,
        "why": ("The ASCII diagram legend. It is the one place that states the per-agent reality "
                "positively rather than by omission. If you edit it, keep the line 66 columns "
                "wide or the box breaks."),
    },
    {
        "file": "guides/tmux-ssh-agents.html",
        "text": "your agents now run in a sealed container per project",
        "count": 2,
        "why": ("MIRRORED: once in the FAQPage JSON-LD and once in the visible answer. A fix that "
                "changes one and not the other leaves the claim live in structured data, which is "
                "what search engines and models read."),
    },
    {
        "file": "use-cases/keep-agents-off-private-files.html",
        "text": "Agents run inside an island, a container denied all host-file access by default.",
        "count": 2,
        "why": ("MIRRORED: FAQPage JSON-LD plus the visible answer. Said 'Each agent runs in a "
                "container' on both until 2026-09-01 — the per-agent claim made in full, using "
                "none of the words the earlier sweeps searched for."),
    },
    {
        "file": "use-cases/keep-agents-off-private-files.html",
        "text": "Agents run inside an island, a container that can't see your host files at all.",
        "count": 1,
        "why": "Same page, body copy. Same history as the pair above.",
    },
    {
        "file": "index.html",
        "text": ("Every privileged crossing is written to a tamper-evident, hash-chained ledger "
                 "you can read, export, and verify"),
        "count": 1,
        "kind": "audit",
        "why": (
            "TRUE ONLY WHILE EVERY LEDGER WRITER IS VOUCHED FOR. Verified 2026-09-01: all "
            "45 non-test ledgerAppend call sites use ProvenanceBrokered (43) or "
            "ProvenanceWitnessed (2), both of which ledger.Provenance.Vouched() accepts. "
            "ProvenanceSelfReported exists and today has no writer — only readers. When one "
            "lands (a headless agent's own account of itself is the expected first), the "
            "hash chain still proves nobody EDITED a row and stops implying a row was TRUE "
            "when written, and this sentence needs qualifying. The TUI's audit banner had "
            "exactly this gap and was qualified in #373; the site is the same claim on "
            "another surface. This pin cannot detect that change on its own — it watches the "
            "page, not the Go — so it is written here to be read by whoever edits the "
            "sentence."
        ),
    },
]


def main() -> int:
    # A check that cannot see a difference must not report agreement.
    if not CLAIMS:
        print("FAIL: the claim manifest is empty, so this guard verifies nothing.")
        return 1
    for c in CLAIMS:
        if len(c["text"]) < 40:
            print(f"FAIL: the pin for {c['file']} is only {len(c['text'])} characters.")
            print("      A fragment that short can match by accident and pins nothing.")
            return 1

    failed = False
    failures = []
    for c in CLAIMS:
        path = ROOT / c["file"]
        if not path.exists():
            print(f"FAIL: {c['file']} does not exist, but a {c.get('kind', 'containment')} claim is pinned in it.")
            failed = True
            failures.append(c)
            continue
        found = io.open(path, encoding="utf-8").read().count(c["text"])
        if found == c["count"]:
            print(f"  ok  {c['file']:<46} {found}x")
            continue

        failed = True
        failures.append(c)
        print()
        if found == 0:
            print(f"FAIL: {c['file']} no longer contains a pinned {c.get('kind', 'containment')} claim.")
            print("      Reworded or deleted. Either is fine ON PURPOSE, and neither is fine by accident.")
        elif found < c["count"]:
            print(f"FAIL: {c['file']} has {found} of {c['count']} copies of a pinned claim.")
            print("      A MIRRORED sentence was changed in one place and not the other. That is")
            print("      exactly how this claim stayed live in structured data after the visible")
            print("      copy was fixed.")
        else:
            print(f"FAIL: {c['file']} has {found} copies of a pinned claim, expected {c['count']}.")
            print("      A new copy appeared. Every copy is a place the claim can drift.")
        print(f"      pinned text: {c['text']}")
        print(f"      why it is true: {c['why']}")
        print("      If you changed it deliberately, update CLAIMS in this file in the SAME")
        print("      commit and say why the new wording is still true. Do not restore the old")
        print("      wording just to go green.")

    if failed:
        if any(c.get("kind", "containment") == "containment" for c in failures):
            print()
            print("The boundary is the ISLAND, which is per PROJECT. An island holds several agents;")
            print("they share it, each on its own git worktree. Any sentence implying a wall between")
            print("agents in one island is false.")
        return 1
    print(f"\n  all {len(CLAIMS)} pinned site claims intact")
    return 0


if __name__ == "__main__":
    sys.exit(main())
