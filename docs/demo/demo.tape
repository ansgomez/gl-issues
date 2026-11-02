# The README screencast, recorded with VHS (https://github.com/charmbracelet/vhs)
# against a made-up GitLab group served by a fake glab: run `make demo`.

Output docs/demo-dark.gif

Set Shell "bash"
Set FontSize 14
Set Width 1240
Set Height 640
Set Padding 18
Set Framerate 15 # a slow machine drops frames at the default 50 and plays too fast
Set Theme "Catppuccin Mocha"
Set TypingSpeed 70ms
Set WindowBar Colorful

Hide
Type '. "$GLI_DEMO/env.sh"' Enter
Sleep 1s
Show

Type "gl-issues" Sleep 400ms Enter
Sleep 2.5s

# the tree starts folded: one label per subgroup, with counts
Type "b" Sleep 2s

# an overdue bug: read it
Down@300ms 4 Sleep 600ms
Enter Sleep 3s
Escape Sleep 800ms

# an unassigned one: take it, set a due date, comment
Down@300ms 2 Sleep 600ms
Type "i" Sleep 1.5s
Type "d" Sleep 800ms
Ctrl+U Type "fri" Sleep 1.2s Enter
Sleep 1.2s
Type "c" Sleep 800ms
Type "Taking this one, PR by Friday." Sleep 800ms
Ctrl+S Sleep 1.8s

# what is overdue or due soon, then search
Type "o" Sleep 2s
Escape Sleep 600ms
Type "/" Sleep 400ms
Type "dark" Sleep 1.5s Enter
Sleep 2s
Escape Sleep 600ms

# compact view, then a tree two group levels deep
Type "z" Sleep 1.5s
Type "l" Sleep 2s

Type "q"
Sleep 600ms
