package server

import "bytes"

// Bracketed paste markers (DEC private mode 2004). The client turns the mode
// on in the real terminal, so every paste reaches the server wrapped in them.
var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// filterPaste adapts input that may hold a bracketed paste to what the pane's
// app asked for: an app with mode 2004 on gets the markers as-is, one without
// it gets the plain text (the markers would otherwise arrive as junk keys).
// Input with no paste in it is returned untouched.
func (p *pane) filterPaste(data []byte) []byte {
	if !bytes.Contains(data, pasteStart) && !bytes.Contains(data, pasteEnd) {
		return data
	}
	if p.vt != nil && p.vt.InputModes().BracketedPaste {
		return data
	}
	data = bytes.ReplaceAll(data, pasteStart, nil)
	return bytes.ReplaceAll(data, pasteEnd, nil)
}

// bracketPaste wraps text for the pane like a terminal paste would: in paste
// markers when its app asked for bracketed paste, unchanged otherwise. An end
// marker inside the text is removed so the paste cannot close itself early and
// have the rest run as typed keys.
func (p *pane) bracketPaste(text []byte) []byte {
	if p.vt == nil || !p.vt.InputModes().BracketedPaste {
		return text
	}
	text = bytes.ReplaceAll(text, pasteEnd, nil)
	out := make([]byte, 0, len(pasteStart)+len(text)+len(pasteEnd))
	out = append(out, pasteStart...)
	out = append(out, text...)
	return append(out, pasteEnd...)
}
