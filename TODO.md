# TODO

## Plex follow-ups

Updated: 2026-10-31

The browser sign-in and sign-out, server discovery, playback reporting, list
refresh, visible Plex like state, cover art, the browser metadata column,
library radio stations, play-context restore, the endless song mix, the album
and artist jumps (Ctrl+O, Ctrl+L), and Ctrl+S downloads are complete.
Playlist write-back, switching servers without a new sign-in, transcoding, and
a hierarchical artist browser (the flat album list with its filter covers it)
are out of scope. Remaining work:

- [ ] Rediscover and fail over to another local, remote, or relay connection
  automatically when the active Plex route dies during a running session.

## Visualizer ideas

- [ ] Album cover visualizer: a visualizer mode built from the cover art of
  the playing track. Think through how it looks and reacts to the audio.
