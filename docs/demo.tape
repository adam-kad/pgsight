# Records docs/demo.gif. Install vhs (https://github.com/charmbracelet/vhs)
# and run from the repository root:
#
#   vhs docs/demo.tape

Output docs/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 640
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms

# Build the current source and put it on PATH, off camera.
Hide
Type "mkdir -p /tmp/pgsight-demo && go build -o /tmp/pgsight-demo/pgsight ./cmd/pgsight && export PATH=/tmp/pgsight-demo:$PATH && clear"
Enter
Sleep 5s
Show

# 1. A plan piped in: jump between hints and open the details panel.
Type "cat testdata/hash_batches.analyze.txt | pgsight"
Sleep 600ms
Enter
Sleep 2.5s
Type "n"
Sleep 1.2s
Enter
Sleep 3s
Type "n"
Sleep 3s
Escape
Sleep 1s
Type "q"
Sleep 800ms

# 2. A bigger plan with a CTE, an InitPlan and a SubPlan: navigate and fold.
Type "pgsight testdata/cte_subplans.analyze.txt"
Sleep 600ms
Enter
Sleep 2.5s
Down@350ms 2
Sleep 800ms
Left
Sleep 800ms
Left
Sleep 1.2s
Down@350ms 4
Sleep 800ms
Enter
Sleep 3s
Escape
Sleep 600ms
Type "g"
Sleep 400ms
Space
Sleep 1.2s
Space
Sleep 1.5s
Type "q"
Sleep 800ms

# 3. Plain output for pipes and CI.
Type "pgsight --plain --fail-on=warning testdata/sort_disk.analyze.txt; echo exit=$?"
Sleep 600ms
Enter
Sleep 4s
