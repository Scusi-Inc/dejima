# Guided first island — pick a repo, name it, choose an agent.
#
# Runs `dejima --demo`. The repo picker shows a synthetic list (no filesystem
# scan, so no real repos leak). The tape walks the guided flow — source → name →
# agent → agent name — and stops BEFORE the real create (there's no daemon in
# demo, and provisioning is slow/undeterministic anyway). It captures the part
# that sells the flow: how little there is to it.
#
# Render:  vhs assets/tapes/04-first-island.tape
# Needs:   `dejima` on PATH (v0.8.32+). No daemon.

Output assets/first-island.gif

Set Shell "bash"
Set FontSize 18
Set Width 1500
Set Height 850
Set Padding 26
Set TypingSpeed 70ms

Hide
Type "dejima --demo"
Enter
Sleep 2.5s
Show

Sleep 1.5s

# [n] opens the guided creator on the repo picker.
Type "n"
Sleep 2s

# Pick the first repo (Enter selects the highlighted one).
Enter
Sleep 1.5s

# Name the island.
Type "storefront"
Sleep 800ms
Enter
Sleep 1.5s

# Choose the first agent (claude-code leads the picker).
Enter
Sleep 1.2s

# Name that agent (optional — blank keeps its id).
Type "frontend"
Sleep 800ms
Enter
Sleep 2s

# We're now on the roster, one keystroke from creating. Stop here — the real
# create needs a daemon, and this is the part worth showing.
Sleep 2.5s

Escape
Sleep 400ms
Escape
Sleep 400ms
Type "q"
Sleep 500ms
