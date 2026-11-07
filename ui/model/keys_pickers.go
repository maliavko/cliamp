package model

import tea "charm.land/bubbletea/v2"

func (m *Model) handleThemeFilterKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.filterKey(&m.themePicker.filterList, "theme-picker-filter", msg, m.themeCount(), m.themePickerRecomputeFilter) {
		m.themePickerApply()
		m.themePickerMaybeAdjustScroll(m.effectivePlaylistVisible())
	}
	return nil
}

// handleThemeKey processes key presses while the theme picker is open.
func (m *Model) handleThemeKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.themePicker.filtering {
		return m.handleThemeFilterKey(msg)
	}

	visible := m.effectivePlaylistVisible()
	if stepListCursor(msg.String(), &m.themePicker.cursor, m.themePickerViewCount(), visible) {
		m.themePickerApply()
		m.themePickerMaybeAdjustScroll(visible)
		return nil
	}
	switch msg.String() {
	case "ctrl+x":
		m.toggleExpandedView()
		m.themePickerMaybeAdjustScroll(m.effectivePlaylistVisible())

	case "enter":
		m.themePickerSelect()

	case "/":
		m.themePicker.beginFilter()
		m.themePickerRecomputeFilter()
		return nil

	case "esc", "q", "t":
		m.themePickerCancel()
	}
	return nil
}

func (m *Model) handleVisPickerFilterKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.filterKey(&m.visPicker.filterList, "visualizer-picker-filter", msg, len(m.visPicker.modes), m.visPickerRecomputeFilter) {
		m.visPickerApply()
		m.visPickerMaybeAdjustScroll(m.effectivePlaylistVisible())
	}
	return nil
}

// handleVisPickerKey processes key presses while the visualizer picker is open.
func (m *Model) handleVisPickerKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.visPicker.filtering {
		return m.handleVisPickerFilterKey(msg)
	}

	// The apply can cross VisNone and resize the rows, so fit the scroll after it.
	if stepListCursor(msg.String(), &m.visPicker.cursor, m.visPickerViewCount(), m.effectivePlaylistVisible()) {
		m.visPickerApply()
		m.visPickerMaybeAdjustScroll(m.effectivePlaylistVisible())
		return nil
	}
	switch msg.String() {
	case "ctrl+x":
		m.toggleExpandedView()
		m.visPickerMaybeAdjustScroll(m.effectivePlaylistVisible())

	case "enter":
		m.visPickerSelect()

	case "/":
		m.visPicker.beginFilter()
		m.visPickerRecomputeFilter()
		return nil

	case "esc", "q", "ctrl+v":
		m.visPickerCancel()
	}
	return nil
}

func (m *Model) deviceMaybeAdjustScroll(visible int) {
	n := len(m.deviceEntries())
	m.devicePicker.cursor = min(max(0, m.devicePicker.cursor), max(0, n-1))
	lines, cursorAt := m.deviceLines()
	m.devicePicker.scroll = clampedScroll(m.devicePicker.scroll, cursorAt, len(lines), visible)
}

// handleDeviceKey processes key presses while the audio device picker is open.
func (m *Model) handleDeviceKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+x":
		m.toggleExpandedView()
		m.deviceMaybeAdjustScroll(m.effectivePlaylistVisible())
	case "up", "k":
		if n := len(m.deviceEntries()); m.devicePicker.cursor > 0 {
			m.devicePicker.cursor--
		} else if n > 0 {
			m.devicePicker.cursor = n - 1
		}
		m.deviceMaybeAdjustScroll(m.effectivePlaylistVisible())
	case "down", "j":
		if n := len(m.deviceEntries()); m.devicePicker.cursor < n-1 {
			m.devicePicker.cursor++
		} else if n > 0 {
			m.devicePicker.cursor = 0
		}
		m.deviceMaybeAdjustScroll(m.effectivePlaylistVisible())
	case "enter":
		entries := m.deviceEntries()
		if m.devicePicker.cursor >= len(entries) {
			return nil
		}
		entry := entries[m.devicePicker.cursor]
		m.devicePicker.visible = false
		if entry.cast != nil {
			return m.castTo(*entry.cast)
		}
		// A local output brings the sound back from a Cast device first.
		back, _ := m.castBack()
		if entry.local.Active {
			return back
		}
		return tea.Batch(back, switchDeviceCmd(entry.local.Name))
	case "esc", "d", "q":
		m.devicePicker.visible = false
	}
	return nil
}
