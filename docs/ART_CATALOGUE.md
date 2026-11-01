# Art catalogue (draft v0.1)

Every image the web app uses is listed here. The build writes the machine-readable copy to `web/assets/manifest.js`. The app loads assets only from this catalogue (PRD D30). An asset is done when it's drawn, exported, and shown correctly on the preview page.

## 1. Rules

- Pixel art only, drawn at native size and scaled by whole numbers with nearest-neighbour scaling.
- The world is drawn at 1280×720 and enlarged 2× at build time to 2560×1440, so buildings are about twice the size of the people and agents fit in front of them. Portraits are not enlarged. The world is 1280×720 in drawing units. The city takes the middle 640×360, and the rest is countryside, mountains and sea to explore.
- The city is laid out along the Sacred Way, one road from the Gates in the west to the Harbour in the east, with the stations numbered in the order work passes them: 1 Gates, 2 Library, 3 Council house, 4 Academy (4b Symposium), 5 Workshop, 6 Oracle right above it, 7 Areopagus, 8 Harbour. The Agora and the Theatre sit off the road. Portraits are 32×32 (and 64×64 for the agent drawer). Icons are 16×16. Role badges are 12×12.
- Every asset uses only the palette in §2. No anti-aliasing and no gradients except by dithering.
- Light comes from the top left. Outlines are deep navy (`ink`), not black.
- The look is Hellenic: whitewashed marble, Aegean blues, gold and olive, with terracotta only on roofs and pottery. The UI uses the same colours: white and navy, Aegean blue for actions, gold for highlights, and a meander strip under the header.
- Each asset with a dusk version has the same shape in both. Only the palette mapping changes.
- Animations are short loops of 2 to 6 frames at 6 to 8 fps. Nothing walks.
- Files live in `web/assets/<group>/<id>.png`. Animated assets are strips named `<id>.strip<frames>.png`. One texture atlas per group is built from these for PixiJS.

## 2. Palette

| Token | Use | Day | Dusk |
|---|---|---|---|
| `ink` | outlines, text on light | #14213d | #0a1426 |
| `ink-soft` | secondary lines | #3d4f6b | #2a3550 |
| `marble` | columns, buildings | #fbfaf6 | #d4d9e8 |
| `marble-shade` | building shadows | #cdd5df | #8e97b4 |
| `stone` | paths, walls | #b9b4a8 | #6f7088 |
| `stone-dark` | deep shadow | #7d786e | #474a63 |
| `terracotta` | roofs, pots | #b5523b | #8f4440 |
| `terracotta-dark` | roof shadow | #7e3424 | #5e2a32 |
| `bronze` | frames, statues | #c49a2c | #9c7d3a |
| `bronze-light` | highlights | #ecd06a | #d9b862 |
| `olive` | trees, grass | #6b8a3a | #40593e |
| `olive-dark` | foliage shadow | #3f5a2c | #26392e |
| `sand` | beach, ground | #eadcb5 | #a69c86 |
| `sea` | water | #1f86c9 | #24508a |
| `sea-deep` | far water | #0e4a8f | #142a5c |
| `foam` | wave tops | #d6f1fb | #9db8dc |
| `sky` | sky | #8fd0f5 | #e7907a |
| `sky-high` | top of sky | #3d8fd8 | #4a3f7a |
| `fire` | forge, torches | #f08a24 | #f59a30 |
| `fire-hot` | flame core | #ffd35a | #ffe08a |
| `smoke` | oracle smoke | #e4e8f2 | #aaa6c6 |
| `ok` | done, pass | #4f9a4a | #5fb05a |
| `warn` | waiting, blocked | #e0a020 | #f0b030 |
| `fail` | failed | #c03a32 | #e0503a |
| `skin` | faces and hands | #e0a878 | #b88068 |
| `skin-shade` | face shadow | #b87850 | #8a5a4a |
| `hair-dark` | dark hair and beards | #3a2a20 | #2a1e1e |
| `hair-grey` | grey hair and beards | #b8b0a8 | #8a8290 |
| `cloth-blue` | robes | #1d4e89 | #26406e |
| `cloth-red` | robes, couches | #a63d33 | #7a2e38 |
| `grass-light` | sunlit grass | #93a84a | #5e6e44 |
| `haze` | sky near the horizon | #d8eefa | #f4b48a |

`ok`, `warn` and `fail` are used only for status, never for scenery.

## 3. Fonts

| Use | Font | Licence |
|---|---|---|
| Headings, place names, bubbles | Silkscreen (Google Fonts) | OFL |
| Body text, Inbox cards, settings | Inter (Google Fonts) | OFL |
| Transcripts, diffs, commands | JetBrains Mono (Google Fonts) | OFL |

## 4. Scene

| ID | Size | Frames | Description |
|---|---|---|---|
| `scene/base` | 1280×720 | 1 | Coastal Greek city on a hill. Sea at the bottom, a mountain at the top right for Delphi, olive groves and paths between the places. Empty plots where buildings go. Day and dusk. |
| `scene/water` | 640×50 | 6 | Wave loop along the shore, laid over the base. |
| `scene/cloud-0..2` | 64×20 | 1 | Three cloud shapes that drift slowly. Hidden at dusk. |
| `scene/birds` | 20×10 | 2 | A small flock that flaps across the sky. |
| `scene/stars` | 640×110 | 2 | Twinkling stars for dusk only. |
| `fx/ufo` | 68×36 (2×) | 4 | Saucer with blinking lights. It brings your messages to an agent. |
| `fx/beam` | 60×144 (2×) | 2 | Shimmering beam the UFO shines down while it delivers. |
| `fx/notes` | 28×30 | 6 | Music notes rising from the DJ. |
| `fx/decks` | 96×40 | 4 | DJ booth on the Theatre stage: two turntables turning, a mixer and blinking lights. |
| `fx/scroll` | 32×24 (2×) | 2 | Sealed scroll carrying a handoff note along the Sacred Way. Click it to read the note. |

## 5. Buildings

Each building has an idle state and an active state (lit windows, open doors, banner up). The active state shows while any agent is working there.

| ID | Size | Frames | Place | Description |
|---|---|---|---|---|
| `bldg/gates` | 64×56 | 1 + active | Intake | Two columns with a lintel and a bronze gate. Active: gate open, banner with the ticket count. |
| `bldg/library` | 80×56 | 1 + active | Survey and research | Wide hall with scroll shelves visible through the columns. Active: lamps lit. |
| `bldg/council` | 64×48 | 1 + active | Scheduler | Small round council house with a stepped roof. Active: smoke from the hearth. |
| `bldg/academy` | 72×56 | 1 + active | Plan | Low building in an olive grove, a sundial out front. Active: a tablet with lines on an easel. |
| `bldg/workshop` | 72×56 | 4 (fire) + active | Build | Stone forge with an anvil and chimney. Fire loops while active, embers only when idle. |
| `bldg/oracle` | 64×72 | 4 (smoke) + pass/fail | Check | Temple on the mountain ledge with a tripod. Smoke loops while checks run. After a run the smoke turns `ok` or `fail` for a few seconds. |
| `bldg/areopagus` | 64×48 | 1 + active | Review | Bare rock hill with stone benches in a half circle. Active: a set of scales on the top bench. |
| `bldg/symposium` | 80×56 | 1 + active | Experiments | Banquet hall with couches and a large krater (wine bowl). Active: torches lit, one couch lit per experiment. |
| `bldg/agora` | 96×48 | 1 | Messages inside a job | Open square with a fountain and a stoa along one side. Bubbles appear here. |
| `bldg/harbour` | 96×56 | 1 | Ship | Quay, wooden pier, amphorae and a storehouse. |
| `bldg/theatre` | 80×52 | 1 + active (2) | Singers | Small hillside theatre with stone tiers and a stage. Active: torches lit while a singer performs. |
| `bldg/trireme-docked` | 48×24 | 1 | Ship | Ship at the pier, sail down. |
| `bldg/trireme-sailing` | 48×32 | 2 | Ship | Sail up, oars moving. Shown for a few seconds when a commit, push or PR goes out. |

## 5a. Landmarks to discover

Landmarks don't host work. They're there to find while you move around. Clicking one for the first time counts as a discovery, which is remembered in the browser. The HUD and the legend show how many you've found, and the legend hides the ones you haven't found yet.

| ID | Size | Frames | Where | Description |
|---|---|---|---|---|
| `bldg/olympus` | 200×150 | 1 + active | far north west | Snowy peak in a ring of clouds with a small golden temple on top. Lightning strikes now and then. |
| `bldg/horse` | 48×44 | 1 | west plain, by the fields | The wooden horse on wheels. |
| `bldg/poseidon` | 80×90 | 1 | east cape, on the cliff | Temple with a bronze trident beside it. |
| `bldg/pharos` | 48×80 | 4 | island in the west sea | Lighthouse whose light sweeps left and right. |
| `bldg/colossus` | 56×84 | 2 | island in the east sea | Bronze giant across two pedestals, torch flickering. |

The countryside also has a forest and striped fields to the west, a vineyard to the east, a far mountain range, rocks in the sea and two ships sailing past.

## 6. The Aegean (many jobs)

Every job is its own city. The Aegean map shows them all: each city-state stands on its own island, and small boats carry Assembly news between cities that are working. Click a city (or its chip in the info box) to go into it; the Aegean chip brings you back out.

| ID | Size | Frames | Description |
|---|---|---|---|
| `map/base` | 960×540 | 1 | Sea with a strip of mainland at the top, six islands for cities and a few rocks. |
| `map/polis` | 64×56 | empty, idle, active (2), done | A city-state seen from far away: walls, houses and a temple. Active flies a waving banner, done wears a laurel wreath, empty is a bare hill with a flag for the next job. |
| `map/boat` | 16×12 | 2 | Small boat carrying an Assembly message. |

Each city has a card under it with its name, ticket, phase, agents, cost and a step bar.

## 7. Portraits

Each philosopher has a 32×32 bust, a 4-frame talking version of it (mouth closed, open, wide, open) used while they speak or sing, and a 16×16 token used when they ride the ship. Busts are shoulders and head, facing three-quarters left, on a plain `marble` disc. The frame and status ring are separate assets (§8), so one portrait works in every state.

| ID | Role | What makes them recognisable |
|---|---|---|
| `portrait/solon` | Scheduler | Older, short grey beard, laurel band, holding a wax tablet |
| `portrait/herodotus` | Researcher | Curly dark beard, travel cloak, scroll under the arm |
| `portrait/thales` | Researcher | Long white beard, looking up, small star above the head |
| `portrait/anaximander` | Researcher | Trimmed beard, holding a round map |
| `portrait/eratosthenes` | Researcher | Thin face, measuring staff with a shadow line |
| `portrait/aristotle` | Planner | Short neat beard, receding hair, bound book |
| `portrait/plato` | Planner | Broad shoulders, full beard, finger pointing up |
| `portrait/archimedes` | Builder | Wild hair, compass in hand |
| `portrait/hero` | Builder | Young, clean shaven, small steam sphere (aeolipile) |
| `portrait/euclid` | Builder | Neat beard, set square |
| `portrait/pythagoras` | Builder | Turban-like headband, triangle on a tablet |
| `portrait/socrates` | Reviewer | Bald, round face, snub nose, thick beard |
| `portrait/diogenes` | Reviewer | Scruffy beard, ragged cloak, lit lamp |
| `portrait/zeno` | Reviewer | Lean face, arrow pointing sideways (his paradox) |
| `portrait/heraclitus` | Reviewer | Stern brow, small flame beside him |
| `portrait/protagoras` | Humanizer | Smiling, speaker's hand raised, red cloak |
| `portrait/isocrates` | Humanizer | Elderly, clean robes, quill |
| `portrait/xenophon` | Finalizer | Soldier's helmet pushed back, writing tablet |
| `portrait/solomun` | DJ | Short dark hair and beard, black shirt, headphones with red cups |
| `portrait/homer` | Narrator | Long grey hair and beard, olive band, white robe |

## 8. Frames and status rings

| ID | Size | Frames | Description |
|---|---|---|---|
| `frame/bronze-32` | 36×36 | 1 | Round bronze frame with a meander (Greek key) rim, for 32×32 portraits. |
| `frame/bronze-64` | 72×72 | 1 | Same frame for 64×64 portraits. |
| `ring/thinking` | 40×40 | 4 | Slow rotating dotted ring in `sky-high`. |
| `ring/tool` | 40×40 | 4 | Rotating ring in `bronze-light` with a small gear. |
| `ring/waiting` | 40×40 | 2 | Pulsing ring in `warn`. |
| `ring/blocked` | 40×40 | 1 | Solid ring in `warn` with a small bar. |
| `ring/done` | 40×40 | 1 | Solid ring in `ok` with a laurel leaf. |
| `ring/failed` | 42×42 | 2 | Ring in `fail`, blinking. |
| `ring/interrupted` | 42×42 | 8 | Rotating ring in `foam`, shown while the UFO delivers a message. |
| `frame/runtime-mark` | 12×12 | 1 | Small coin on the frame rim that holds a runtime glyph (§9). |

## 9. Icons (16×16)

Runtime glyphs are simple letters or symbols drawn in pixel style. They don't copy any company logo.

| Group | IDs |
|---|---|
| Status | `icon/thinking`, `icon/tool`, `icon/waiting`, `icon/blocked`, `icon/done`, `icon/failed` |
| Runtime | `icon/rt-pi` (π glyph), `icon/rt-claude` (C glyph), `icon/rt-codex` (X glyph) |
| Model tier | `icon/tier-think` (owl), `icon/tier-code` (hammer), `icon/tier-execute` (sandal) |
| Media | `icon/video`, `icon/screenshot`, `icon/trace`, `icon/report`, `icon/diagram` |
| Inbox | `icon/inbox-checks` (tripod), `icon/inbox-plan` (scroll with seal), `icon/inbox-question` (messenger), `icon/inbox-tests` (shield), `icon/inbox-conflict` (crossed spears), `icon/inbox-done` (laurel) |
| Symposium | `icon/vote` (pebble), `icon/winner` (laurel wreath), `icon/disqualified` (broken pot), `icon/branch` (olive twig) |
| Files | `icon/file-read`, `icon/file-changed`, `icon/file-blocked` (padlock) |
| Actions | `icon/pause`, `icon/stop`, `icon/message`, `icon/restart`, `icon/pin`, `icon/expand`, `icon/close` |
| Nav | `icon/nav-inbox`, `icon/nav-jobs`, `icon/nav-settings`, `icon/start` |

## 10. Role badges (12×12)

| ID | Role | Symbol |
|---|---|---|
| `badge/scheduler` | Scheduler | wax tablet and stylus |
| `badge/researcher` | Researcher | scroll |
| `badge/planner` | Planner | compass |
| `badge/builder` | Builder | hammer |
| `badge/executor` | Executor | sandal |
| `badge/reviewer` | Reviewer | scales |
| `badge/humanizer` | Humanizer | quill |
| `badge/finalizer` | Finalizer | trireme |

## 11. UI chrome

All of these are 9-slice images, so they stretch to any size without blurring the corners.

| ID | Corner size | Description |
|---|---|---|
| `ui/bubble` | 6 | Speech bubble, `marble` fill, `ink` outline, with a tail as a separate 8×6 piece. |
| `ui/bubble-think` | 6 | Rounder bubble with small trailing dots, for "thinking" captions. |
| `ui/card` | 4 | Parchment card for media, diagrams and Inbox items. |
| `ui/panel` | 8 | Drawer and settings panel, marble with a meander border. |
| `ui/button` | 4 | Bronze button, with normal, hover, pressed and disabled states. |
| `ui/input` | 4 | Recessed stone input box for the task ID field. |
| `ui/tag` | 3 | Small pill for ticket IDs, branch names and models. |
| `ui/divider` | none | 16×4 meander strip that repeats. |

## 12. App identity

| ID | Size | Description |
|---|---|---|
| `app/icon` | 16, 32, 64, 512 | Owl of Athena on a bronze coin. Used as the favicon and app icon. |
| `app/empty-state` | 160×90 | Quiet harbour at dawn with no ships, shown when no jobs are running. |
| `app/loading` | 32×32 | Spinning bronze coin, 6 frames. |

## 13. How the art is made

All art is drawn in code ahead of time, and agents never make art while working on tickets (PRD D30). The source lives in `art/` and the output in `web/assets/`, which is generated and can be rebuilt at any time:

```
go run ./cmd/artbuild        # draws everything into web/assets/
open web/assets/preview.html # or serve web/ and open /assets/preview.html
```

### 13.1 One palette, one drawing per asset

- `art/palette.go` holds every colour, with a day and a dusk value. It's the only place colours are defined.
- Every asset is palette-indexed: each pixel stores a palette index, not a colour.
- The build writes a day file and a dusk file for each asset by swapping the palette. Each asset is drawn once, and the two versions can't drift apart.

### 13.2 Three ways of drawing, one per kind of asset

| Kind | How | Where |
|---|---|---|
| Scenery and buildings | Go drawing code with a small set of tools: rectangles, lines, polygons, ellipses, ordered dithering and an automatic ink outline. Shared pieces like `column`, `temple` and `OliveTree` keep the style consistent. | `art/scene.go`, `art/buildings.go` |
| Portraits | Generated from each philosopher's traits: hair, beard, colours, headband and prop. Changing a look means changing one line. Each philosopher gets a 32×32 bust for cards and a 16×16 token for the scene. | `art/portraits.go` |
| Icons, badges, glyphs | Text grids (`.px`), one character per pixel, with a legend that maps characters to palette names. Easy to diff and to fix one pixel at a time. | `art/src/icon/*.px` |

### 13.3 The build

`cmd/artbuild`:

- draws every asset and every state (for example `bldg/oracle` has `idle`, `running`, `pass` and `fail`)
- writes animated states as horizontal strips, with the frame count in the manifest
- stops with an error if a text grid has a wrong row width or an unknown character, or if two states of one asset differ in size
- writes `web/assets/manifest.js` with every asset, the scene layout and the philosophers
- copies the preview page

### 13.4 The preview page

`web/assets/preview.html` has two tabs:

- Scene: the full landscape filling the window, with zoom and pan, and a Play button that runs every city through its job from the Gates to the Harbour. Agents glide between places, the camera follows the work, and names and bubbles are real text. Clicking a philosopher opens the panel with live thoughts and a box to send it information, which brings in the UFO. Day and dusk switch with one button.
- Catalogue: every asset and state, animated, at ×2 or ×4, with its ID.

## 14. Scene setups

The app never draws anything new while it runs. It picks a prepared setup that matches what the job is doing and fills in the agents. A setup sets which buildings are in which state, which agents stand at which place with which status ring, their speech bubbles, the message lines between them, and the ship.

| Setup | When | Scene |
|---|---|---|
| No jobs | nothing running | quiet city, ship docked |
| Ticket arrives | job started | Gates open with the banner, Solon reading the ticket |
| Research | survey and research | Library lamps lit, researchers in front |
| Plan | planning | Academy windows lit with the easel out, Solon at the Council house hearth |
| Symposium | experiments running | banquet hall torches lit, one builder per experiment |
| Build | building | forge fire burning, builders at the Workshop |
| Checks running | checks running | Oracle smoke rising |
| Checks failed | a check failed | red smoke, failing builder with a red ring |
| Checks passed | all checks passed | green smoke, green rings |
| Review | review | scales on the Areopagus, reviewers on the benches, message lines to builders |
| Needs you | waiting on the Inbox | the asking agent at the Agora with a blocked ring |
| Conversation | two agents talk | the speaker walks over and stands next to the listener. They take turns, the speaker's portrait animates its mouth, and each line shows as a rounded speech bubble and in both agents' thoughts |
| DJ set | you press DJ set | Solomun steps behind the decks at the Theatre, torches light, coloured stage lights sweep, notes rise and the mix plays with sound. Up to three idle philosophers come over and dance |
| Handoff | a phase ends | a scroll flies along the road from the station that finished to the next one; clicking it shows the note, and the receiving building's popup keeps the latest notes. If the note was missing, the agent who owes it comes back with a "Writing my handoff" bubble first |
| Interrupt | you sent an agent information | the UFO flies in, hovers above the agent and beams the message down, the agent's ring turns `interrupted`, then the UFO leaves |
| Ship | finalize | Xenophon walks onto the ship, and it sails out of the Harbour with him on board |

Several setups can be true at once in a real job, for example review on the Areopagus while a builder fixes a finding in the Workshop. The app combines them: each place takes the state from whatever is happening there.

## 14a. Music

Music lives in `web/songs/`, one folder per DJ, listed in `web/songs/songs.js`. Solomun plays `solomun/hous-mix.mp3`. The DJ set button in the header starts and stops the set with sound, the DJ's bubble shows the time played and the total, and the position is saved in the browser so the next set continues where the last one stopped (this needs a server that supports range requests; the plain Python preview server restarts from the beginning).

## 14c. Narration

Homer, the narrator, sits in a caption at the bottom of the map and tells in plain words what is happening and why, for example: "The Symposium is being prepared. Aristotle found three workable approaches and none is clearly better, so Solon opened three experiment branches, one builder each." He speaks on every phase change, missing handoff, message you send, process you stop and DJ set. Clicking the caption, or the Chronicle button, opens the full history per city, newest first.

## 14b. Legend and explanations

- Legend (header button): three tabs. Places shows each building with what happens there, and clicking one moves the map to it. People lists every character by role with what they do, and clicking one who is working opens their panel. Signs explains the status rings, the UFO, the notes and the sailing ship.
- Clicking a building on the map opens a small popup with its name, what happens there and who is there now.
- The legend texts live in `art/legend.go`, and the build fails if a place or character has no text.

## 15. Built so far (first pass)

| Group | Done | Still to draw |
|---|---|---|
| Scene | base, water loop, stars, clouds, birds, UFO and beam | none |
| Buildings | all 13 including the Theatre, with every state | none |
| Portraits | 18 philosophers and 2 placeholder singers, as 32×32 busts with talking frames and 16×16 tokens | real singer designs |
| Frames and rings | bronze frames (32, 16), 7 status rings including interrupted | runtime coin on the rim |
| Icons | status (6), runtime glyphs (3) | model tier, media, Inbox, Symposium, files, actions, nav |
| Role badges | none | all 8 |
| UI chrome | none | all 8 |
| Map view | none | all 6 |
| App identity | none | owl icon, empty state, loading coin |

## 16. Count

| Group | Assets |
|---|---|
| Scene | 4 |
| Buildings | 12 |
| Map | 6 |
| Portraits | 18 (×2 sizes) |
| Frames and rings | 9 |
| Icons | 41 |
| Role badges | 8 |
| UI chrome | 8 |
| App | 3 |
| Total | 109 distinct assets, about 130 files with sizes and dusk versions |
