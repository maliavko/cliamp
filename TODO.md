# TODO

## Plex follow-ups

Updated: 2026-11-01

The browser sign-in and sign-out, server discovery, playback reporting, list
refresh, visible Plex like state, cover art, the browser metadata column,
library radio stations, play-context restore, the endless song mix, the album
and artist jumps (Ctrl+O, Ctrl+L), Ctrl+S downloads, and route failover are
complete.
Playlist write-back, switching servers without a new sign-in, transcoding, a
hierarchical artist browser (the flat album list with its filter covers it),
and an album cover visualizer are out of scope. No Plex work remains.

## Ctrl+R after a restart

Ctrl+R in the playlist does nothing after cliamp restores a provider queue at
start, such as a Plex playlist. The resume state saves the tracks but not the
playlist ID, so `refreshesInPlace` finds no open playlist to reload. Save the
ID with the context and restore `activeProviderPlaylistID` from it. Until
then, reopening the playlist from the provider pane makes Ctrl+R work again.
