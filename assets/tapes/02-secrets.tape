# Per-island secrets — add one inline.
#
# Runs `dejima --demo`. The secrets pane shows a synthetic set (EXPO_TOKEN,
# NPM_TOKEN) with fixed fingerprints; adding one is local-only, so nothing real
# is stored and the value is masked as you type. Deterministic and safe.
#
# Render:  vhs assets/tapes/02-secrets.tape
# Needs:   `dejima` on PATH (v0.8.29+). No daemon.

Output assets/secrets.gif

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

# `$` opens the secrets pane for the highlighted island.
Type "$"
Sleep 2.5s

# Add a secret: [a] → name → value (masked).
Type "a"
Sleep 1.2s
Type "STRIPE_SECRET_KEY"
Sleep 800ms
Enter
Sleep 1.2s

# The value is hidden as bullets — never echoed.
Type "sk_live_51H8xQ2eZvKf9mNp"
Sleep 1s
Enter
Sleep 2.5s

# It lands in the list, and the RESTART TERMINALS notice explains that a
# running process keeps its old environment.
Sleep 3s

Escape
Sleep 500ms
Type "q"
Sleep 500ms
