# TODO

## Plex follow-ups

Updated: 2026-10-27

The browser sign-in and sign-out, server discovery, playback reporting, list
refresh, visible Plex like state, cover art, the browser metadata column,
library radio stations, and play-context restore are complete. Playlist
write-back, switching servers without a new sign-in, and transcoding are out
of scope. Remaining work, in suggested order:

- [ ] Rediscover and fail over to another local, remote, or relay connection
  automatically when the active Plex route dies during a running session.
- [ ] Add artist browsing (`Artists -> Albums -> Tracks`) instead of relying
  only on the flat album list and search.

## Navigation ideas

- [ ] "Go to album": from a track in the queue or search results, open its
  album in the provider browser. Think through the key, the providers that
  can do it, and where Esc returns.
- [ ] "Go to artist": the same for the track's artist, once artist browsing
  exists.
