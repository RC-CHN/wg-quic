# Desktop interaction and visual design

The desktop app helps a person answer three questions: which tunnel am I using,
is it connected, and what can I do next? WireGuard authentication, saved versus
running configuration and unknown outcomes must remain truthful.

## Information hierarchy

- A persistent sidebar owns tunnel selection, search, import and creation.
- The selected tunnel has one primary connection control. Editing is secondary;
  checking and deleting are in an explicit more-actions disclosure.
- Overview shows connection state, transfer, latency and peers. Diagnostics has
  transport internals, telemetry export, file location and disconnection history.
- Empty states explain the next action in the main content area. Sidebar empty
  states remain compact. Searching never discards a draft or changes a connection.
- An editor has a fixed heading and action bar around one scrollable form.
  Local identity, remote peer and optional settings are separate groups. Saving
  validates but never silently connects or restarts a running tunnel.

## Visual system

Use native system fonts including Chinese UI fonts. Body text is 14 px with a
1.5 line height; supporting text is at least 12 px. Strong headings, readable
secondary text and spacing establish hierarchy, without decorative counters,
all-caps labels or gradients. Use a restrained blue accent for actions, green
for authenticated connections, amber for attention and red for errors/deletion.
States always have text, not color alone. Both light and dark palettes must be
reviewed. Controls have consistent 40 px height, visible keyboard focus and
explicit labels. Technical keys and paths wrap or truncate with full text
available; they must never force a horizontal page scrollbar.

## Interaction rules

- Preserve draft content, focus, selection and expanded disclosures on refresh.
- Keyboard navigation in the tunnel list belongs to that list, not the page.
- Buttons announce work in progress; failed writes retain the draft. Errors
  reveal their field even inside a collapsed group and support keyboard focus.
- Generated keys are the initial draft, not a user edit. Changing a field makes
  the draft dirty. Cancel or leaving asks only if there are actual changes.
- Route presets are explicit user choices; never infer and replace custom
  routes, keys or endpoints. Transport settings apply to the whole tunnel.
- Copy a public key without revealing private keys. Source mode is explicit and
  warns that configuration text contains secrets.
- Test minimum 920 × 620 and default 1180 × 760 windows, both languages/themes,
  long names, multiple peers, empty/search states, errors and keyboard operation.
  Screenshots use synthetic fixtures; no production credentials or private data.

Peer cards label receive and send rates separately from their cumulative byte
counters. Missing samples show a measuring state; a measured zero remains zero.
The copy action sits beside a shortened public key (both ends retained). Handshake
age uses the selected language; the exact local timestamp is available on hover.
Typography uses shared sizes and colors for titles, values, labels and secondary
information. Brand slogans and redundant introductory labels are omitted.
