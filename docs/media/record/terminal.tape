# flovr in a terminal, with no model key: check a workflow, then run its
# tests on the answers recorded for it. record.sh runs it with vhs, with
# RECORD_BIN holding flovr and RECORD_SITE a copy of the examples.
Output "terminal.gif"
Set Shell "bash"
Set FontFamily "JetBrains Mono, JetBrainsMonoNL Nerd Font, Menlo, monospace"
Set FontSize 19
Set LineHeight 1.35
Set Width 1120
Set Height 520
Set Padding 30
Set WindowBar Colorful
Set WindowBarSize 44
Set BorderRadius 12
Set TypingSpeed 42ms
Set Theme {"name":"flovr","black":"#0c0d0a","red":"#f87171","green":"#a3e635","yellow":"#facc15","blue":"#60a5fa","magenta":"#a78bfa","cyan":"#22d3ee","white":"#f1f3ea","brightBlack":"#686d55","brightRed":"#fca5a5","brightGreen":"#bef264","brightYellow":"#fde68a","brightBlue":"#93c5fd","brightMagenta":"#c4b5fd","brightCyan":"#67e8f9","brightWhite":"#ffffff","background":"#0c0d0a","foreground":"#f1f3ea","selection":"#2b2e22","cursor":"#a3e635"}

Hide
Type "cd $RECORD_SITE && export PATH=$RECORD_BIN:$PATH && unset OPENROUTER_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY && export PS1='$ ' && clear"
Enter
Show
Sleep 600ms
Type "flovr validate examples/triage-ticket"
Sleep 400ms
Enter
Sleep 1.4s
Type "flovr test examples/triage-ticket --replay"
Sleep 400ms
Enter
Sleep 3.5s
