package main

import (
	"strings"
	"unicode/utf16"
)

func hasMention(text string, entities []entity, username string) bool {
	units := utf16.Encode([]rune(text))
	for _, e := range entities {
		if e.Type == "mention" && e.Offset >= 0 && e.Length > 0 && e.Offset <= len(units) && e.Length <= len(units)-e.Offset {
			value := string(utf16.Decode(units[e.Offset : e.Offset+e.Length]))
			if strings.EqualFold(value, "@"+username) {
				return true
			}
		}
	}
	return false
}
