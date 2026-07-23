# Get a file to an agent — attach a local file into a live session.
#
# This one CANNOT use --demo: it shows a real in-island session and the upload
# path (the same upload + inject that a drag-and-drop triggers). So it needs a
# real daemon with a running island. Set that up first:
#
#   dejima init --repo <some repo>       # or reuse an existing island
#   # name it "sandbox" (or edit ISLAND below)
#   printf 'notes for the agent\n' > /tmp/notes.txt
#
# Then:  vhs assets/tapes/03-file-to-agent.tape
#
# What it shows: open a contained shell in the island, press Ctrl-] (the attach
# chord), type a local path — it uploads into the island and the in-island path
# is injected. A drag-and-drop does exactly this without the chord.

Output assets/file-to-agent.gif

Set Shell "bash"
Set FontSize 18
Set Width 1500
Set Height 850
Set Padding 26
Set TypingSpeed 70ms

# --- edit these for your setup ---
# ISLAND=sandbox   LOCALFILE=/tmp/notes.txt

Hide
Type "dejima connect sandbox --shell"
Enter
Sleep 3s
Show

Sleep 1.5s
Type "ls"
Enter
Sleep 1.5s

# Ctrl-] — the attach-file chord. A minibuffer prompts for a local path.
Ctrl+]
Sleep 1.5s

# Type the path of a file on THIS machine. It uploads into the island…
Type "/tmp/notes.txt"
Sleep 1s
Enter
Sleep 2.5s

# …and the in-island path is injected — the agent can now read it. A file
# dragged onto the session does the same thing, no chord needed.
Sleep 2.5s

# Detach (Ctrl-\ returns to the dashboard when launched from it; here just exit).
Type "exit"
Enter
Sleep 800ms
