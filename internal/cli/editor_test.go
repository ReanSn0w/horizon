package cli

import (
	"bufio"
	"strings"
	"testing"
)

func TestEditorKeyProtocols(t *testing.T) {
	tests := []struct {
		name  string
		input string
		kind  keyKind
		text  string
	}{
		{"legacy enter", "\r", keySubmit, ""},
		{"portable newline", "\n", keyNewline, ""},
		{"legacy cancel", "\x03", keyCancel, ""},
		{"kitty shift enter", "\x1b[13;2u", keyNewline, ""},
		{"kitty enter", "\x1b[13;1u", keySubmit, ""},
		{"kitty Unicode text", "\x1b[1076;1;1076u", keyText, "д"},
		{"xterm shift enter", "\x1b[27;2;13~", keyNewline, ""},
		{"left", "\x1b[D", keyLeft, ""},
		{"delete", "\x1b[3~", keyDelete, ""},
		{"multiline paste", "\x1b[200~первая\r\nвторая\x1b[201~", keyText, "первая\nвторая"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := readKey(bufio.NewReader(strings.NewReader(test.input)))
			if err != nil {
				t.Fatal(err)
			}
			if event.kind != test.kind || string(event.text) != test.text {
				t.Fatalf("event = kind %d text %q", event.kind, event.text)
			}
		})
	}
}

func TestEditBufferUnicodeAndVerticalMovement(t *testing.T) {
	buffer := &editBuffer{}
	buffer.insert([]rune("абв\n12\nxyz"))
	buffer.cursor = 2
	buffer.moveVertical(1)
	if buffer.cursor != 6 {
		t.Fatalf("move down cursor = %d", buffer.cursor)
	}
	buffer.moveVertical(1)
	if buffer.cursor != 9 {
		t.Fatalf("second move down cursor = %d", buffer.cursor)
	}
	buffer.backspace()
	buffer.delete()
	buffer.insert([]rune("Ю"))
	if string(buffer.text) != "абв\n12\nxЮ" {
		t.Fatalf("edited buffer = %q", buffer.text)
	}
	buffer.moveVertical(-1)
	if buffer.cursor != 6 {
		t.Fatalf("move up cursor = %d", buffer.cursor)
	}
}
