# terminal-fidelity

Objective: inside ngmux, apps (Claude Code, nvim, fzf, build tools) must look and behave as in a plain terminal (audit P1, 2026-10-21).
Branches: one per slice, stacked on `fix/pty-input-queue`, each PR to `main` (strategy: stacked-to-main, chosen by the user). Test runner: `go test -race ./...`.

## Specs

- S1 Text styles: SGR 2 (dim), 8 (hidden), 9 (strikethrough) and their resets (22, 28, 29) reach the real terminal. Audit wording: "Claude Code usa *dim* para todo el texto gris → dentro de ngmux se ve plano. Eso es el \"quita diseño\"".
- S2 Reverse video with explicit colors renders reversed (today setChar swaps FG/BG and render also emits SGR 7, cancelling out).
- S3 Bold no longer forces colors 0-7 to their bright variant; the real terminal decides, as without ngmux.
- S4 Cursor shape (DECSCUSR, `CSI Ps SP q`) set by the focused pane's app is shown by the real terminal.
- S5 Synchronized output (DEC mode 2026) from an inner app: ngmux does not paint a half-drawn frame while the app holds the mode (bounded by a timeout).
- S6 Insert mode (IRM, `CSI 4 h/l`) and REP (`CSI Ps b`) are implemented.
- S7 OSC 52 written by an inner app reaches the real terminal's clipboard (honouring `set-clipboard off`); OSC strings are no longer truncated at 256 runes.
- S8 A paste while the Ctrl-b which-key panel or the Ctrl-b m overlay is up does not leak text into the pane.
- S9 Scrollback capture cost (`vterm.pushHistory`, ~50% CPU on log floods) is reduced; baseline `BenchmarkWriteLogFlood` 16.87 MB/s, 291795 lines/s.

## Tasks

| ID | Specs | Slice / branch | Route | Status | Commit |
|----|-------|----------------|-------|--------|--------|
| T1 | S1-S3 | feat/text-styles | inline | done | 43ddbf6 (RED: dim/hidden/strike attr 0x0, reverse fg/bg swapped, bold fg 9; GREEN full suite -race) |
| T2 | S4 | feat/text-styles | inline | done | 4815565 (RED: CursorStyle 0 after ESC[5 q, e2e no ESC[5 q; GREEN incl. e2e detach restores ESC[0 q) |
| T3 | S5 | feat/sync-output | inline | done | f27bf42 (RED: mid-update row "ABC" want "A", no DECRQM reply; GREEN) |
| T4 | S6 | feat/sync-output | inline | done | 0d10da6 (RED: IRM row "XYc", REP row "a-|"; GREEN + 20s FuzzTermOps clean) |
| T5 | S7 | feat/inner-clipboard | inline | open | |
| T6 | S8 | fix/overlay-paste | inline | open | |
| T7 | S9 | perf/scrollback-capture | inline | open | |

Route evidence: no Writer trigger (sequential units sharing vt10x/vterm/render files; no context backstop yet), so the parent writes inline.

## Log

- L1 (2026-10-21, user): "Si por favor sigue con el P1"
- L2 (2026-10-21, user): PR chain strategy "Encadenados a main (Recomendado)".
- L3 Forecast: ~800-1000 authored changed lines across 5 slices.
- L4 T1+T2 done on feat/text-styles; full `go test -race ./...` green, Windows build ok. Extra fix found while exploring: setChar pre-swapped FG/BG for reverse while render also sent SGR 7 (reverse cancelled). CSI sequences with an intermediate byte other than DECSCUSR are now ignored instead of running as the plain command.
- L5 T3+T4 done on feat/sync-output. Sync freeze bounded by syncTimeout=200ms; DECRQM answers 2004/2026. Full suite -race green, Windows build ok.
