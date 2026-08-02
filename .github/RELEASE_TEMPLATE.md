## What's New in v0.7.2

### Android Fixes
- **Smooth scrolling** — PDF and EPUB now use native momentum/inertia scrolling instead of JavaScript drag-pan
- **Status bar overlap fixed** — proper edge-to-edge rendering with system bar insets
- **Highlighter drag fixed** — reading guide now moves correctly when unlocked (touch-action + preventDefault)
- **PDF tap toggles toolbar** — tapping the PDF page now shows/hides the toolbar (was only working in EPUB)
- **Device picker improved** — compares page + chapter + scroll, not just page number

### Sync Improvements
- **Unique device IDs** — each machine gets a random ID stored in localStorage, preventing two PCs from sharing the same progress row
- **Server timestamp guard** — delayed syncs can't overwrite fresher data from other devices
- **Auth on page close** — `beforeunload` now sends JWT token (was silently failing with 401)
- **Periodic cloud sync** — progress syncs on every save, not just on close (app crashes no longer lose progress)

### UI Improvements
- **Title below toolbar** — book title moved under the tool buttons for better readability
- **Tap to toggle chrome** — single tap shows/hides toolbar (no hover needed)
- **Popup dismiss fix** — tapping content closes panels, tapping inside panels does not
- **Zoom controls in popup** — zoom in/out moved to a popup panel (like fonts)
- **Upload button icon-only** — removed text label for cleaner look
- **Offline status indicator** — green/orange dot shows online/offline state

### Offline Support
- **Sync queue** — failed progress POSTs saved to localStorage, retried on reconnect
- **OPDS catalog cache** — last successful catalog shown when offline
- **Upload queue** — failed uploads remembered by file path, retried on reconnect

### Server
- **Device-aware progress** — per-device reading positions with conflict resolution
- **OPDS XML fix** — strips embedded HTML from Calibre-Web feeds

### CI/CD
- **Unified releases** — all workflows upload to a single GitHub Release per tag
- **No duplicate builds** — CI only runs on PRs, releases on tag push only
