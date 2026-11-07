package model

import (
	"github.com/bjarneo/cliamp/internal/cast"
	"github.com/bjarneo/cliamp/player"
)

// deviceEntry is one row of the "Play on" picker that can be chosen: a local
// audio output or a Cast device.
type deviceEntry struct {
	local *player.AudioDevice
	cast  *cast.Device
}

// deviceEntries lists the local outputs, then the Cast devices.
func (m Model) deviceEntries() []deviceEntry {
	entries := make([]deviceEntry, 0, len(m.devicePicker.devices)+len(m.devicePicker.castDevices))
	for i := range m.devicePicker.devices {
		entries = append(entries, deviceEntry{local: &m.devicePicker.devices[i]})
	}
	for i := range m.devicePicker.castDevices {
		entries = append(entries, deviceEntry{cast: &m.devicePicker.castDevices[i]})
	}
	return entries
}

// deviceLines renders the picker under its section headers and returns the
// line of the cursor. A dot marks where the sound plays: the active output
// while nothing casts, or the Cast device.
func (m Model) deviceLines() ([]string, int) {
	var lines []string
	cursorAt, entry := 0, 0
	add := func(label string) {
		if entry == m.devicePicker.cursor {
			cursorAt = len(lines)
		}
		lines = append(lines, cursorLine(label, entry == m.devicePicker.cursor))
		entry++
	}

	lines = append(lines, dimStyle.Render("  This device"))
	switch {
	case m.devicePicker.loading:
		lines = append(lines, loadingLine("Loading devices…"))
	case len(m.devicePicker.devices) == 0:
		lines = append(lines, dimStyle.Render("  No audio output devices found."))
	}
	for _, d := range m.devicePicker.devices {
		label := d.Description
		if label == "" {
			label = d.Name
		}
		if d.Active && !m.casting() {
			label += " " + activeToggle.Render("●")
		}
		add(label)
	}

	lines = append(lines, "", dimStyle.Render("  Cast"))
	switch {
	case m.devicePicker.castSearching && len(m.devicePicker.castDevices) == 0:
		lines = append(lines, loadingLine("Searching the network…"))
	case m.devicePicker.castErr != "" && len(m.devicePicker.castDevices) == 0:
		lines = append(lines, dimStyle.Render("  Search failed: "+m.devicePicker.castErr))
	case len(m.devicePicker.castDevices) == 0:
		lines = append(lines, dimStyle.Render("  No Cast devices found."))
	}
	track, idx := m.currentPlaybackTrack()
	castable := idx < 0 || !m.player.IsPlaying() || m.canCast(track)
	for _, d := range m.devicePicker.castDevices {
		label := d.Name
		if d.Model != "" && d.Model != d.Name {
			label += dimStyle.Render(" · " + d.Model)
		}
		if m.casting() && m.cast.player.Device().ID == d.ID {
			label += " " + activeToggle.Render("●")
		} else if !castable {
			label += dimStyle.Render(" (Plex tracks only)")
		}
		add(label)
	}
	return lines, cursorAt
}
