// Package rot13 keeps words out of plain sight in the source, so that code
// search does not surface them; it hides nothing from a reader who decodes.
package rot13

import "strings"

// String rotates the Latin letters of s by 13 places; applied twice it returns s.
func String(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			return 'A' + (r-'A'+13)%26
		}
		return r
	}, s)
}
