# Fleet dashboard — the hero clip.
#
# Runs `dejima --demo`: a synthetic fleet, no daemon, no real repos/paths/secrets.
# The agent states churn on a timer (working → needs-you → idle), so the fleet
# looks alive. Fully deterministic and safe to publish.
#
# Render:  vhs assets/tapes/01-fleet-dashboard.tape
# Needs:   `dejima` on PATH (v0.8.20+). No daemon.

Output assets/fleet-dashboard.gif

Set Shell "bash"
Set FontSize 18
Set Width 1500
Set Height 850
Set Padding 26
Set TypingSpeed 60ms

Hide
Type "dejima --demo"
Enter
Sleep 2.5s
Show

# Let the fleet render and breathe — the status column animates on its own.
Sleep 3s

# Expand every island to reveal the agents beneath each.
Type "E"
Sleep 3s

# Walk down through the tree so the selection highlight moves across agents.
Down
Sleep 800ms
Down
Sleep 800ms
Down
Sleep 800ms
Down
Sleep 1.2s

# Sit on the live fleet a beat so the "needs you"/idle states cycle on screen.
Sleep 3.5s

Type "q"
Sleep 500ms
