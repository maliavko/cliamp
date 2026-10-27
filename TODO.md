# TODO

## Player state restoration

Updated: 2026-10-25

- [ ] Restore the previous player state after restart:
  - selected source/provider;
  - active playlist and current track;
  - volume;
  - playback position within the current track.
  Persist state safely during playback and on exit, tolerate missing providers,
  playlists, and tracks, and avoid starting playback automatically unless the
  existing autoplay/resume policy explicitly calls for it.

## Radio follow-ups

Updated: 2026-10-25

- [ ] Import custom radio stations from local or remote M3U/M3U8 and PLS
  files into `radios.toml`. Reuse the existing playlist parsers, show a
  preview before writing, preserve station names when present, merge without
  duplicates, and report entries that are not usable stream URLs. Consider
  XSPF or OPML later if real station collections require them.

## Cover art follow-ups

Updated: 2026-10-27

- [ ] Hide cover art in terminals without image support instead of drawing
  the ANSI half-block fallback.
- [ ] Try showing the cover in the full-screen visualizer view.

## Plex follow-ups

Updated: 2026-10-26

The browser sign-in, server discovery, playback reporting, list refresh, and
visible Plex like state are complete. Remaining work, in suggested order:

- [ ] Consider a debounced selected-album cover preview in provider browsers.
  Reuse the now-playing artwork cache and renderer, cancel stale selection
  loads, and add it only where it does not reduce useful list space.
- [ ] Rediscover and fail over to another local, remote, or relay connection
  automatically when the active Plex route dies during a running session.
- [ ] Add playlist write-back: create and delete playlists, add and remove
  tracks, and reorder playlist entries on Plex.
- [ ] Add artist browsing (`Artists -> Albums -> Tracks`) instead of relying
  only on the flat album list and search.
- [ ] Restore the last open Plex album or playlist and playback position after
  restart, matching the Jellyfin and Emby experience.
- [ ] Allow switching to another discovered Plex server without repeating
  browser authorization.
- [ ] Add account lifecycle controls: sign out, replace an account, and safely
  remove locally stored Plex credentials.
- [ ] Add configurable remote-playback quality and optional Plex transcoding
  for slow links and bandwidth-limited relay connections.
