package telegram

func SplitMessage(text string) []string {
	result := []string{}
	runes := []rune(text)
	// Telegram's entity positions use UTF-16. A 4000-unit budget also fits
	// the 4096-character sendMessage limit when astral characters occur.
	for len(runes) > 0 {
		units, end := 0, 0
		for end < len(runes) {
			cost := 1
			if runes[end] > 0xffff {
				cost = 2
			}
			if units+cost > 4000 {
				break
			}
			units += cost
			end++
		}
		result = append(result, string(runes[:end]))
		runes = runes[end:]
	}
	return result
}
