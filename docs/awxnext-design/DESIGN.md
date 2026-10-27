# awxnext — Heatmap signature: design handoff

Goal: replace the generic "Linear-clone" look with one owned visual signature — the **heatmap cell** (one host × one task × one moment) — reused across the app as its identity, not only as a chart on the job page.

Reference mockups are in `mockups/` (`*.dc.html`). They are design-tool sources: they won't render standalone, but the markup, inline styles and the data logic at the bottom of each file are the spec. Read them for layout, spacing and copy.

---

## 1. Design tokens

Replace the current palette and fonts with these. Remove the indigo primary everywhere.

```css
:root {
  /* ground & chrome */
  --paper:   #F3F1EA;  /* app background (was white/grey) */
  --surface: #FFFFFF;  /* cards, tables */
  --ink:     #15171A;  /* text, top bar, primary borders */
  --ink-2:   #3C4047;  /* secondary text */
  --muted:   #4A4E55;  /* meta text — 4.5:1 on white and paper */
  --line:    #E2DED3;  /* card borders, dividers */
  --line-2:  #EDEAE2;  /* row dividers */

  /* signal accent — ONLY for "live" and the single primary action per screen */
  --lime:    #C8F03C;

  /* cell states — differ in lightness, not just hue */
  --cell-ok:      #2D6A4F;
  --cell-changed: #F2B33D;
  --cell-failed:  #E4572E;  /* always with a white diamond mark */
  --cell-skipped: #D6D2C6;
  --cell-empty:   #E9E6DD;  /* no run in that slot */
  /* not run = transparent + 1px dashed #BDB7A9 */
  /* running = lime + inset 2px ink ring, gentle pulse */

  /* status text colours on light backgrounds */
  --text-failed: #B23A16;
  --pill-failed-bg: #FBE3DA; --pill-failed-fg: #8E2A10;
  --pill-warn-bg:   #FDF0D2; --pill-warn-fg:   #6E4A00;
}
```

Fonts (Google Fonts):
- **Bricolage Grotesque 700** — page titles only (`h1`, ~30–34px, letter-spacing −0.025em).
- **Schibsted Grotesk 400/500/600** — all UI and prose, base 14px.
- **JetBrains Mono 400/500** — anything a machine named: host names, job numbers, playbook paths, revisions, timestamps, counts.

Rule of thumb: mono for names the machine gave; sans for words people wrote.

## 2. The Cell component (build this first)

One reusable component used everywhere: `<Cell state size />`.

- `state`: `ok | changed | failed | skipped | empty | not-run | running`
- `size`: `xs` 9px, `sm` 12px, `md` 16px, `lg` 30px (job matrix), `fill` (width from grid, fixed height ~20–22px)
- `border-radius: 2px` (3px at lg). `box-sizing: border-box`.
- `failed` draws a white rotated square (diamond) centred, ~1/3 of the cell. Never rely on colour alone.
- `running`: lime fill, `box-shadow: inset 0 0 0 2px var(--ink)`, opacity pulse 1.4s. Respect `prefers-reduced-motion`.
- Every cell in a matrix is a real `<button>` with an `aria-label` like `"rbx-nbo-02, task 7 Wait for VMI ready: failed"` and a tooltip with the same text; clicking lands on the output.
- A row of cells gets `role="img"` + a summary `aria-label` when cells are not individually interactive.

Matrix rules:
1. A cell always means a result. Never use it as decoration (logo excepted).
2. Time runs left → right, oldest → newest.
3. Rows are hosts or templates; columns are tasks or time.
4. Every interactive cell opens the corresponding output.

## 3. App shell

- **Top bar** (ink, 52px): logo = 3×3 grid of 6px cells (lime, green, green / green, amber, green / green, green, lime) + "awxnext" in Bricolage 700 18px. Search with `Ctrl K`. Right side: on the Now page a strip of the last 12 jobs as 8×18 cells, then `1 running · 0 queued` in mono, avatar button in lime.
- **Sidebar** (≈212px, paper background, right border `--line`): section labels small caps 10.5px. Active item = bold ink text + a 6px ink square marker on the left. **No indigo highlight block.**
- **Remove the secondary column** on Templates (Starred / Saved views / Folders). Replace with a chip row (see §5).
- Content `max-width: ~1180px`, padding 28px 36px. Tables never stretch across 2560px.
- Primary button: lime background, ink text, 1px ink border. Secondary: white, ink border, inverts on hover.
- Timestamps: 24h, date + timezone (`27.10.2026 05:14:02 CET`). Make the format a user setting later.

## 4. Screen: Now  (`mockups/Main.dc.html`)

- Title "Now" + one mono status line: `Tue 27.10.2026 · 10:34 CET · 41 runs in 24 h · 2 failed · 1 running`. No big KPI tiles.
- Range chips: 24 h / 7 d / 30 d, inventory filter.
- **Fleet heatmap**: rows = templates (name + inventory in mono), columns = 24 hourly buckets (label every 3 h), last column = `runs · fail`. Each cell = worst result in that bucket. Clicking a cell opens the runs in that bucket. Legend top-right.
- **Running now**: per running job, a live hosts × tasks mini matrix (md cells), current task highlighted as `running`, plus `TASK 6 / 9`, task name, hosts done, ETA.
- **Needs attention**: each failed run as a tiny matrix (xs cells) + `host · task N · task name` in failed colour + one-line reason.

## 5. Screen: Templates  (`mockups/Templates.dc.html`)

- Title + `8 templates · 1 failing`; lime "New template" button.
- Chip row replaces the secondary sidebar: `All 8`, `★ Starred 2`, `Run by me 5`, `Last run failed 1`, divider, `Folder: all ▾`, dashed `+ Save current filter`.
- Query search keeps the existing syntax hint.
- Columns: star · Template (name + playbook in mono) · **Last 20 runs** (strip of 9×18 cells, old → new, empty slots dashed) · Inventory · Last run (small cell + status word + `#id · time`) · Schedule (`every 3 h`, `hourly`, or `—`; never "Manual") · Launch.
- Clicking a row (or Enter) expands an inline preview: last run as a numbered hosts × tasks matrix + numbered task list + "Open run →".
- Footer hints: `j/k move · enter expand · l launch · s star · / filter`.

## 6. Screen: Job detail  (`mockups/Job.dc.html`)

- Header: template name (Bricolage), status pill phrased by scope (`Failed on 1 of 6 hosts`), actions `Relaunch`, lime `Relaunch on failed host`, `⋯`.
- Mono meta line with full date, duration, launcher, revision, inventory link, host count, EE, node.
- **Matrix**: columns are **numbers** (1…N), never rotated labels; task names in a numbered list under the matrix (two columns). Column numbers and list items for tasks containing a failure are shown in failed colour. Host names link to the host page. Cells 30px buttons; selected cell gets a 2px ink outline. Arrow keys move the selection.
- **Detail panel** beside the matrix for the selected cell: state cell, `host · TASK n`, task name, duration, a comparison line (`Only this host failed · 5 others ok`), the cell's output in a dark `pre`, links "Jump to line in output" and "This task on this host, last 30 runs →" (goes to host page).
- **Output log**: each task header prefixed by a number badge coloured with that task's worst state, followed by a per-state summary (`5 ok · 1 failed`). Failed tasks expanded by default with a dark red row tint. Filters: Failed only, Changed, Collapse all, Show prompts (the `SSH password:` line is hidden unless this is on). Long warning lines wrap.

## 7. Screen: Host  (`mockups/Host.dc.html`) — new

- Host name in mono as the title; flag pills (`Failing since #409`, `1 task not idempotent`).
- Mono facts line: OS, vCPU, RAM, python, last contact, groups.
- **Task history matrix**: rows = every task that touched this host, grouped by template; columns = 30 time buckets of 4 h (5 days); day labels. Right column shows automatic flags:
  - **changes every run** — task reported `changed` on 100% of its runs → likely not idempotent (row tinted).
  - **new failure** — latest run failed after previous successes (row tinted).
  - **drift <when>** — a single `changed` between `ok` runs with no template/revision change.
- "What the matrix is telling you" card: one sentence per flag, linking to the job.
- Host facts card (inventory, groups, ansible_host, enabled, last job, facts cached).

Backend note: this needs per-host, per-task results across jobs. Check that job events (`runner_on_ok/changed/failed/skipped` with `host`, `task`, `play`, `role`) can be aggregated cheaply — probably a materialised table keyed by (host_id, template_id, task_name, job_id) rather than scanning raw job events.

## 8. Settings (not mocked, apply the same system)

- Single column per section, label + help on the left, control on the right, grouped (General, Activity stream, Analytics, Proxy).
- Backend key (`ACTIVITY_STREAM_ENABLED`) shown small, muted, click-to-copy.
- Put **Base URL of the service** first and warn when empty.
- Units on numeric fields (`14400 seconds (4 h)`).

## 9. Done when

- No indigo left in the codebase; tokens above are the only colour source.
- One `Cell` component powers the logo, run strips, fleet heatmap, job matrix, host history and mini matrices.
- Every matrix works with keyboard and screen reader, and is readable in greyscale.
- Templates page has no secondary sidebar; Job page has no rotated labels.
