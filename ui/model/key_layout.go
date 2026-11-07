package model

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// keyboardEnhancements asks a terminal with the kitty keyboard protocol to
// send every key as an escape code that names the key's place on a US
// keyboard and the text it types, so latinKey can tell them apart.
var keyboardEnhancements = tea.KeyboardEnhancements{
	ReportAlternateKeys:        true,
	ReportAllKeysAsEscapeCodes: true,
	ReportAssociatedText:       true,
}

// cyrillicKeys maps the letters of the Russian ЙЦУКЕН layout, and the extra
// Ukrainian and Belarusian ones, to the US key at the same place. It serves
// terminals that do not name that key.
var cyrillicKeys = map[rune]rune{
	'й': 'q', 'ц': 'w', 'у': 'e', 'к': 'r', 'е': 't', 'н': 'y', 'г': 'u',
	'ш': 'i', 'щ': 'o', 'з': 'p', 'х': '[', 'ъ': ']', 'ф': 'a', 'ы': 's',
	'в': 'd', 'а': 'f', 'п': 'g', 'р': 'h', 'о': 'j', 'л': 'k', 'д': 'l',
	'ж': ';', 'э': '\'', 'я': 'z', 'ч': 'x', 'с': 'c', 'м': 'v', 'и': 'b',
	'т': 'n', 'ь': 'm', 'б': ',', 'ю': '.', 'ё': '`',
	'і': 's', 'ї': ']', 'є': '\'', 'ў': 'o',
}

// usShifted maps a US key to the character it types with Shift.
var usShifted = map[rune]rune{
	'`': '~', '1': '!', '2': '@', '3': '#', '4': '$', '5': '%', '6': '^',
	'7': '&', '8': '*', '9': '(', '0': ')', '-': '_', '=': '+', '[': '{',
	']': '}', '\\': '|', ';': ':', '\'': '"', ',': '<', '.': '>', '/': '?',
}

// latinKey turns a key pressed in a non-Latin layout into the US key at the
// same place, so the hotkeys work in any layout. A key that types a
// non-Latin character always turns. A digit or punctuation key turns only
// while nonLatin says the layout is non-Latin, as the Russian one, which
// types "." on the US "/" key. Latin layouts such as Dvorak or AZERTY keep
// their own keys. A terminal with the kitty keyboard protocol names the US
// key in BaseCode; otherwise Cyrillic letters are looked up in cyrillicKeys.
// It reports false when the key stays as it is.
func latinKey(msg tea.KeyPressMsg, nonLatin bool) (tea.KeyPressMsg, bool) {
	var base rune
	switch {
	case msg.Code > unicode.MaxASCII && unicode.IsPrint(msg.Code):
		base = msg.BaseCode
		if base == 0 {
			base = cyrillicKeys[unicode.ToLower(msg.Code)]
		}
	case nonLatin && unicode.IsPrint(msg.Code) && !unicode.IsLetter(msg.Code) && msg.Code != ' ':
		base = msg.BaseCode
		if base == 0 && msg.ShiftedCode != 0 {
			// The terminal names no other key, so this is the US one;
			// only its shifted character can differ.
			base = msg.Code
		}
	}
	if base == 0 || base > unicode.MaxASCII {
		return msg, false
	}
	shifted := msg.Mod.Contains(tea.ModShift) || unicode.IsUpper(msg.Code) ||
		(msg.Text != "" && unicode.IsUpper([]rune(msg.Text)[0]))
	latin := msg
	latin.Code, latin.BaseCode, latin.ShiftedCode = base, 0, 0
	if shifted {
		if s, ok := usShifted[base]; ok {
			latin.ShiftedCode = s
		} else {
			latin.ShiftedCode = unicode.ToUpper(base)
		}
	}
	if latin.Text != "" {
		latin.Text = string(base)
		if shifted {
			latin.Text = string(latin.ShiftedCode)
		}
	}
	if latin.String() == msg.String() {
		return msg, false
	}
	return latin, true
}

// noteLayout records whether the letter keys type Latin letters, which
// latinKey needs to place digit and punctuation keys.
func (m *Model) noteLayout(msg tea.KeyPressMsg) {
	if unicode.IsLetter(msg.Code) {
		m.nonLatinLayout = msg.Code > unicode.MaxASCII
	}
}
