package model

import "unicode"

// Words counts the words of the text pieces; codes are not words and break
// a word like a space does. A word is a run of letters, digits and marks
// that may hold ' ’ - inside, and has at least one letter or digit.
func Words(ps []Piece) int {
	n := 0
	for _, p := range ps {
		if !p.Code {
			n += wordsIn(p.Text)
		}
	}
	return n
}

func wordsIn(s string) int {
	n, in := 0, false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !in {
				n++
				in = true
			}
		case in && (unicode.IsMark(r) || r == '\'' || r == '’' || r == '-'):
			// stays inside the word
		default:
			in = false
		}
	}
	return n
}
