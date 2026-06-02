# Role State Transitions

This document describes how each role moves through a night turn, including every branch the engine can take: an action submitted within the timer, an action **not** submitted (timeout), an action **blocked** by the Consort, a one-shot ability that is already **spent**, a **dead** role, and the validation rejections that can occur.

A unifying idea runs through all of this: **any turn whose holder cannot take an effective action is a _phantom_ turn** — it narrates and then goes straight to a ponder, skipping the act window entirely. "Cannot act" covers three cases that the engine treats identically: the role has **no living holder** (dead), a one-shot ability is **spent** (an out-of-bullets Vigilante), or the holder was **roleblocked** by the Consort this night. Because the phantom ponder is a single randomized 5–10s beat, an observer can't tell *which* of those reasons applies — so a block is no longer a timing tell. See `roleTurnIsPhantom` in [`state.go`](../internal/game/state.go).

The engine itself is **timeless** — it only knows sub-phase *order*. All wall-clock durations are owned by the room layer ([`internal/room/config.go`](../internal/room/config.go)); the values quoted here come from the `Default*` constants there. The night state machine lives in [`internal/game/rules_phase.go`](../internal/game/rules_phase.go) and [`internal/game/rules_night.go`](../internal/game/rules_night.go); per-role behaviour lives in [`internal/game/rolespec.go`](../internal/game/rolespec.go).

## Contents

- [The night, end to end](#the-night-end-to-end)
- [The per-role turn skeleton](#the-per-role-turn-skeleton)
- [Sub-phase durations](#sub-phase-durations)
- [The NightAction validation gate](#the-nightaction-validation-gate)
- [Mafia](#mafia)
- [Detective](#detective)
- [Doctor](#doctor)
- [Consort (optional)](#consort-optional)
- [Vigilante (optional)](#vigilante-optional)
- [Villager](#villager)
- [Night resolution order](#night-resolution-order)

---

## The night, end to end

A night is a fixed **opening** beat followed by one turn per role in the wake queue, then resolution. The wake order is:

> Mafia → [Consort] → Detective → [Vigilante] → Doctor

`[Consort]` and `[Vigilante]` are **optional** roles (host toggles before the game starts); when enabled they each take a villager slot. The queue is built from the *dealt-time* toggles, not the live roster, so a dead (or spent, or promoted) role's turn still runs — as a **phantom** — to keep the moderator's audio cadence and to avoid leaking who is still alive.

The flow:

1. `BeginNight` enters `PhaseNight` at the **opening** sub-phase.
2. When the opening beat elapses, the engine pops the first role from the wake queue and starts its turn.
3. Each role walks the five-step turn skeleton below (`narrate → act → ponder → sleep → settle`, with phantom turns skipping `act`).
4. When a turn's `settle` ends, the engine pops the next role and repeats — or, if the queue is exhausted, runs `resolveNight` and emits a `PhaseChanged` into Day Discussion.

---

## The per-role turn skeleton

Every role walks the same five-step skeleton: `narrate → act → ponder → sleep → settle`. The only decisions are:

1. **narrate → act vs narrate → ponder** — `act` opens only when the turn has an *actionable* holder. A turn is **phantom** (skips `act`, goes straight to `ponder`) when the role has no living holder, its one-shot action is spent (an out-of-bullets Vigilante), **or** its holder was roleblocked this night. See `roleTurnIsPhantom` in [`state.go`](../internal/game/state.go).
2. **act → ponder** — driven by the actor's submission (`NightAction`), an explicit decline-to-act (`NightPass`, opt-in per role — today only the Vigilante's "hold fire"), or the act-window timer expiring (`AdvancePhase`). All three are deliberately indistinguishable downstream: same cadence, so observers can't tell a submit from a pass from a timeout.

Written out, the transitions are:

- `narrate` → `act` — holder is alive, able, and unblocked.
- `narrate` → `ponder` — phantom turn (dead, spent, or blocked); no act window.
- `act` → `ponder` — action submitted within the timer, **or** `NightPass` (opt-in roles), **or** the timer expired with nothing submitted.
- `ponder` → `sleep` → `settle` → next role (or resolve the night).

A blocked holder learns of the block via a private `Blocked` event delivered **when the cannot-act ponder begins** — i.e. just *after* their narrate cue, mirroring the old "told at the start of your beat" timing. The client shows the notice and keeps the picker hidden.

---

## Sub-phase durations

Source of truth: `internal/room/config.go`. The engine emits `Deadline = 0`; the room stamps the real wall-clock deadline before broadcasting, so server and clients agree.

| Sub-phase | Duration | Notes |
| --- | --- | --- |
| `opening` | 7s | Once per night, before any role. |
| `narrate` | 2.5s | Universal "wake up" cue. |
| `narrate` (Mafia, Day 0) | 4s | Extra "recognize each other" beat. |
| `narrate` (Mafia, Day 1+) | 1.5s | Shorter per-night line. |
| `act` | 60s | Normal action window. Only a turn with an actionable holder reaches it. |
| `ponder` (real, most roles) | 2s | Post-submit breath. |
| `ponder` (real, detective) | 3s | Sized to read the result modal. |
| `ponder` (phantom) | 5–10s (random) | Hides *why* the turn was inert (dead/spent/blocked). |
| `sleep` | 2s | "Go to sleep" cue. |
| `settle` | 3s | Post-sleep beat before the next role. |

There is **no shortened "blocked" act window** — a blocked actor never reaches `act` at all; their turn is phantom. The phantom ponder is **randomized** specifically so an observer can't deduce from timing why the turn produced no action: a dead, spent, and blocked role are all indistinguishable.

---

## The NightAction validation gate

Before any role-specific logic runs, every `NightAction` passes a generic gate in `applyNightAction` ([`rules_night.go`](../internal/game/rules_night.go)). This is where most rejection branches live. The checks run in order; the first one that fails returns its error and nothing is recorded:

1. **Phase is Night?** — otherwise `ErrWrongPhase` (or `ErrGameEnded`).
2. **Actor known and alive?** — otherwise `ErrUnknownPlayer` / `ErrPlayerDead`.
3. **Actor's role has a night action?** — a villager has none, so `ErrNotYourAction`.
4. **Actor's role == current night role?** — otherwise `ErrNotYourTurn` (the strict turn-order gate).
5. **Sub-phase == act?** — a submission during narrate / ponder / sleep / settle is `ErrNotYourTurn`.
6. **Non-mafia AND blocked tonight?** — `ErrBlocked` (a backstop; see below).
7. **Target non-empty, known, and alive?** — otherwise `ErrUnknownPlayer` / `ErrPlayerDead`.
8. **Role-specific `Validate` hook** — may reject (`ErrSelfTarget` / `ErrNotYourAction` / `ErrAlreadyActed`).
9. **Actor already acted this night?** — otherwise `ErrAlreadyActed`.
10. **Pass** — record the action, emit events, and advance `act → ponder`.

> The **blocked** check (step 6 → `ErrBlocked`) is now a defense-in-depth backstop. A blocked actor's turn is phantom, so it never enters `act`; the sub-phase gate (step 5) already rejects any bypassing submit with `ErrNotYourTurn` before control reaches step 6. The branch survives only in case the phantom routing is ever bypassed.

The per-role `Validate` hook (step 8) is:

- **Mafia** — target may not be another mafioso (`ErrNotYourAction`).
- **Consort / Detective / Vigilante** — may not target self (`ErrSelfTarget`).
- **Vigilante** — bullet already spent (`ErrAlreadyActed`, a backstop; see below).
- **Doctor** — none (self-save is allowed).

### NightPass — explicit decline-to-act

A separate command, **`NightPass`**, lets a holder end their act window *early* without acting — the engine side of the Vigilante's "hold fire" button. It's opt-in per role via the spec's `AllowPass` flag (today only the Vigilante), and passes a much shorter gate in `applyNightPass`: PhaseNight, a living actor whose role has `AllowPass` set (else `ErrNotYourAction`), that role is the current night role, and the sub-phase is `act` (else `ErrNotYourTurn`). It records **nothing** — no `pendingNight` entry, so no resource is spent — and simply advances `act → ponder`, identical to a timeout. Mafia are deliberately excluded: their turn is faction-collective, so one mafioso must not be able to end the kill window for everyone.

---

## Mafia

- **Faction:** Mafia. **Always first** in the queue and **never phantom** — the game ends the instant living mafia hits zero, so a night never begins without a living mafioso.
- **Immune to the Consort block.**
- **Faction-collective:** any living mafioso may submit during the act window; the **first** submission locks the kill target and closes the window for the whole faction.

**Turn transitions:** `narrate → act` (the 60s window always opens, since the mafia is never phantom). `act → ponder` either when a mafioso submits a kill or when the 60s timer expires with no kill that night. Then `ponder → sleep → settle`.

During `act`:

- The first submission emits `NightActionRecorded` (faction-only) and ends the window for the whole faction.
- A second mafioso submitting after that is rejected with `ErrNotYourTurn`.
- Targeting a fellow mafioso is rejected with `ErrNotYourAction`.
- Mafia are immune to the Consort block (there is no shortened window).

**On resolution:** the mafia kill resolves **first**. A doctor save on the same target cancels it **silently** (no event is emitted); otherwise the target dies (`PlayerKilled`, public).

---

## Detective

- **Faction:** Town. Always-on reserved role.
- **Blockable** by the Consort.
- Result is delivered **immediately and privately** at submit time (it does not wait for resolution).

**Turn transitions:**

- `narrate → act` — detective alive and unblocked; the 60s window opens.
- `narrate → ponder` (phantom, 5–10s) — detective is dead, **or** blocked this night; no act window.
- `act → ponder` — investigates a target, or the 60s timer expires with no read.
- Then `ponder → sleep → settle`.

When the detective submits, the engine emits `NightActionRecorded` plus a private `DetectiveResult`. Self-investigation is rejected with `ErrSelfTarget`.

When the turn is phantom because of a **block**: there is no act window, and a private `Blocked` event arrives as the ponder begins (just after narrate); the client hides the picker. A bypassing submit is rejected with `ErrNotYourTurn`, and **no** `DetectiveResult` is produced.

> An un-promoted Consort reads as **not mafia** (she is `FactionConsort`); only after she is promoted to `RoleMafia` does she read as mafia.

---

## Doctor

- **Faction:** Town. Always-on reserved role.
- Wakes **last of all**, after both night-killers (the mafia and, when enabled, the vigilante), so the save is the final beat of the night.
- **Blockable** by the Consort.
- **Self-save is allowed** on any night.

**Turn transitions:**

- `narrate → act` — doctor alive and unblocked; the 60s window opens.
- `narrate → ponder` (phantom, 5–10s) — doctor is dead, **or** blocked this night; no act window.
- `act → ponder` — saves a target (possibly self), or the 60s timer expires with no save.
- Then `ponder → sleep → settle`.

When the doctor submits, the engine emits `NightActionRecorded`. The save is reconciled during resolution: if it matches a kill target, that kill is cancelled **silently** — **no** confirmation event is emitted, not even to the doctor. The doctor gets no per-night signal at all and can only infer a successful save from who survives the night. This holds even when two killers (the mafia *and* the vigilante) both hit the saved player: `resolveHit` runs once per attacker, and each is a silent no-op.

When the turn is phantom because of a **block**: there is no act window, and a private `Blocked` event arrives as the ponder begins (just after narrate); the client hides the picker. No save is ever recorded, so the kill on the doctor's intended target lands.

---

## Consort (optional)

- **Faction:** `FactionConsort` — mafia-aligned for *winning*, but her own knowledge group (she neither sees nor appears in mafia coordination). She does **not** count toward the mafia's parity win (that threshold is the *strict* `RoleMafia` count vs the town); the town must still eliminate her to win, and if the cabal is wiped while she lives she is promoted to `RoleMafia` and counts from then on.
- Wakes **right after the mafia** (only if enabled).
- **Never blocked herself** (she is the only blocker, and she acts before the town roles).
- Promotion: if the entire mafia cabal is wiped while she lives, she is promoted to `RoleMafia` (private `ConsortPromoted` + `MafiaRosterRevealed`).

**Turn transitions:**

- `narrate → act` — consort alive; the 60s window opens.
- `narrate → ponder` (phantom, 5–10s) — consort is dead **or** has been promoted away (no living `RoleConsort` holder); no act window.
- `act → ponder` — blocks a target, or the 60s timer expires with no block.
- Then `ponder → sleep → settle`.

When the consort submits, the engine emits `NightActionRecorded`. Self-block is rejected with `ErrSelfTarget`. Blocking a mafioso is legal but has no effect. The block runs **first** in resolution and nullifies the target's action — unless the target is mafia.

> When a promoted Consort no longer holds `RoleConsort`, her old turn keeps running as a phantom so the cadence doesn't shorten and leak the takeover.

---

## Vigilante (optional)

- **Faction:** Town. Wakes after the detective, **before the doctor** (only if enabled), so the doctor can still save the vigilante's target.
- **One bullet for the whole game.**
- **Blockable** by the Consort — and a block nullifies the shot **without spending the bullet**.
- May **hold fire**: an explicit `NightPass` (the client's "Hold fire" button) ends the act window early without firing, keeping the bullet for a later night and sparing the table the full 60s wait.
- Once the bullet is spent, the Vigilante's turn becomes a **phantom** (no act window), indistinguishable from a dead role.

**Turn transitions:**

- `narrate → act` — vigilante alive, bullet unspent, **and** unblocked; the 60s window opens.
- `narrate → ponder` (phantom, 5–10s) — the bullet is already **spent**, the vigilante is **dead**, **or** he is **blocked** this night; no act window.
- `act → ponder` — fires the one bullet, **or** holds fire (`NightPass`, bullet preserved), **or** the 60s timer expires (no shot, bullet preserved).
- Then `ponder → sleep → settle`.

During `act`: firing emits `NightActionRecorded` and schedules the shot. Self-target is rejected with `ErrSelfTarget`. "Hold fire" (`NightPass`) ends the turn early with no shot recorded, so the bullet is preserved. Firing, holding fire, and timing out are indistinguishable to observers.

The reason a turn is phantom changes what it means for the bullet:

- **Spent** — the single bullet was fired on an earlier night, so there is no act window. The UI tells the vigilante his bullet is spent. A bypassing submit is rejected with `ErrNotYourTurn`; the spec's `ErrAlreadyActed` check is an unreachable backstop.
- **Blocked** — no act window; a private `Blocked` event arrives as the ponder begins (just after narrate). No shot is recorded, so the bullet is **not** spent — it is preserved for a later night.

**On resolution (order matters):**

1. The mafia kill resolves first. If the mafia targeted the Vigilante and he wasn't saved, **he dies and his shot never lands** (mafia precedence).
2. The Vigilante's shot resolves second, **only if he is still alive**. A doctor save on the Vigilante's target cancels the shot **silently** (no event) — but the **bullet is still spent**.

The one-shot flag (`vigilanteShotUsed`) is set during resolution **only if a shot was actually recorded**, so a *blocked* Vigilante keeps his bullet.

**Effect on the win condition:** a loaded Vigilante is the one town resource that can remove a mafioso *outside* the daytime vote, and `checkWin` (intentionally) does **not** special-case him. The mafia win only when the **strict** `RoleMafia` count *strictly outnumbers* the living town faction (`mafia > town`), plus the 1-mafia-vs-1-town endgame (which the lone townsperson can never convert). **Exact parity with two or more mafia is not an instant win** — the game plays on. That is precisely what gives a doctor + loaded Vigilante room to work: at `2 mafia vs {doctor, loaded Vigilante}` the doctor keeps the Vigilante alive through the mafia kill, the bullet drops a mafioso below parity, and the town then out-votes the survivor. If the town has no such line, the mafia's next kill simply pushes the board to `mafia > town` and the game ends a cycle later — so the rule stays free of role-specific cases at the cost of not ending a few decided parities a turn early. See `checkWin`.

---

## Villager

- **Faction:** Town. **No night action** and **never in the night queue.**

A villager is never summoned at night, so there is no turn and no state to walk. A `NightAction` from a villager is rejected with `ErrNotYourAction`, regardless of sub-phase. Villagers participate only in the day — they vote and can be voted against — and they count toward the town win.

---

## Night resolution order

After the last role's `settle`, `resolveNight` reconciles every scheduled intent into actual deaths/saves and the public events. Phases run in a fixed order so roles that depend on each other see consistent state:

1. **Block** (`nightPhaseBlock`) — the Consort's block is recorded; blocked non-mafia actions are skipped in the later phases.
2. **Schedule** (`nightPhaseSchedule`) — the mafia kill, doctor save, and vigilante shot are recorded as intents (no state mutated yet).
3. **Resolve** — reconcile the intents into at most one death per target:
   1. The **mafia kill** resolves first; the target dies unless the doctor saved it.
   2. The **vigilante shot** resolves second, and only if the shooter is still alive; a doctor save on its target wastes the shot.
4. **Reveal** (`nightPhaseReveal`) — info roles (the detective) read the resolved state.
5. **Spend bullet** — `vigilanteShotUsed` is set if a shot was actually recorded this night.

**Events emitted during the night (by visibility):**

| Event | Visibility | When |
| --- | --- | --- |
| `NightSubPhaseStarted` | public | every sub-phase boundary (carries `Phantom`) |
| `NightActionRecorded` | faction-only **for the mafia**; **private to the actor** for solo roles (detective / doctor / vigilante / consort) | an actor submits within the act window |
| `DetectiveResult` | private (detective) | at the detective's submit time |
| `Blocked` | private (blocked actor) | as the blocked actor's cannot-act ponder begins (just after their narrate) |
| `PlayerKilled` | public | a kill lands during resolution |
| `PhaseChanged` | public | night resolves into Day Discussion |

> `NightActionRecorded` is scoped to `FactionMafia` only for the mafia (co-mafia must see the locked kill to coordinate). Solo town/consort roles share a faction with non-actors, so scoping their ack to the faction would leak the hidden role — they get a private self-ack instead.
>
> **`ConsortPromoted` / `MafiaRosterRevealed`** are emitted privately to the consort whenever a cabal wipe leaves her the last mafia-aligned player standing. Both callers run `promoteConsortIfNeeded` before the win check: a **lynch** (`applyFinalizeVotes`), and **night resolution** (`resolveAndExitNight`) — the latter because the Vigilante's shot is the one way a mafioso can die at night. Without the night-path promotion the takeover would silently fail and the `RoleMafia` turn would phantom forever.
