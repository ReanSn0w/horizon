package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

type terminalEditor struct {
	in  *os.File
	out *os.File
}

type keyKind int

const (
	keyUnknown keyKind = iota
	keyText
	keySubmit
	keyNewline
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp
	keyDown
	keyCancel
)

type keyEvent struct {
	kind keyKind
	text []rune
}

type editBuffer struct {
	text   []rune
	cursor int
}

func newTerminalEditor(in, out *os.File) MessageEditor {
	return &terminalEditor{in: in, out: out}
}

func (e *terminalEditor) ReadMessage() (result string, resultErr error) {
	inFD, outFD := int(e.in.Fd()), int(e.out.Fd())
	if !term.IsTerminal(inFD) || !term.IsTerminal(outFD) {
		return "", errors.New("interactive editor requires terminal stdin and stderr; use -m or redirected stdin")
	}
	oldState, err := term.MakeRaw(inFD)
	if err != nil {
		return "", fmt.Errorf("enter terminal raw mode: %w", err)
	}
	defer func() {
		fmt.Fprint(e.out, "\x1b[?2004l\x1b[>4;0m\x1b[<u\x1b[?1049l")
		if restoreErr := term.Restore(inFD, oldState); resultErr == nil && restoreErr != nil {
			resultErr = fmt.Errorf("restore terminal mode: %w", restoreErr)
		}
	}()
	// Alternate screen keeps editor rendering out of stdout and shell history.
	// Kitty CSI-u and xterm modifyOtherKeys are opt-in and restored on exit.
	fmt.Fprint(e.out, "\x1b[?1049h\x1b[?2004h\x1b[>4;2m\x1b[>25u")
	reader := bufio.NewReader(e.in)
	buffer := &editBuffer{}
	for {
		renderEditor(e.out, buffer)
		event, err := readKey(reader)
		if err != nil {
			return "", err
		}
		switch event.kind {
		case keyText:
			buffer.insert(event.text)
		case keySubmit:
			return string(buffer.text), nil
		case keyNewline:
			buffer.insert([]rune{'\n'})
		case keyBackspace:
			buffer.backspace()
		case keyDelete:
			buffer.delete()
		case keyLeft:
			if buffer.cursor > 0 {
				buffer.cursor--
			}
		case keyRight:
			if buffer.cursor < len(buffer.text) {
				buffer.cursor++
			}
		case keyUp:
			buffer.moveVertical(-1)
		case keyDown:
			buffer.moveVertical(1)
		case keyCancel:
			return "", ErrInterrupted
		}
	}
}

func (b *editBuffer) insert(value []rune) {
	value = append([]rune(nil), value...)
	b.text = append(b.text, make([]rune, len(value))...)
	copy(b.text[b.cursor+len(value):], b.text[b.cursor:len(b.text)-len(value)])
	copy(b.text[b.cursor:], value)
	b.cursor += len(value)
}

func (b *editBuffer) backspace() {
	if b.cursor == 0 {
		return
	}
	copy(b.text[b.cursor-1:], b.text[b.cursor:])
	b.text = b.text[:len(b.text)-1]
	b.cursor--
}

func (b *editBuffer) delete() {
	if b.cursor == len(b.text) {
		return
	}
	copy(b.text[b.cursor:], b.text[b.cursor+1:])
	b.text = b.text[:len(b.text)-1]
}

func (b *editBuffer) moveVertical(direction int) {
	lineStart := b.cursor
	for lineStart > 0 && b.text[lineStart-1] != '\n' {
		lineStart--
	}
	column := b.cursor - lineStart
	if direction < 0 {
		if lineStart == 0 {
			return
		}
		previousEnd := lineStart - 1
		previousStart := previousEnd
		for previousStart > 0 && b.text[previousStart-1] != '\n' {
			previousStart--
		}
		b.cursor = previousStart + min(column, previousEnd-previousStart)
		return
	}
	lineEnd := b.cursor
	for lineEnd < len(b.text) && b.text[lineEnd] != '\n' {
		lineEnd++
	}
	if lineEnd == len(b.text) {
		return
	}
	nextStart := lineEnd + 1
	nextEnd := nextStart
	for nextEnd < len(b.text) && b.text[nextEnd] != '\n' {
		nextEnd++
	}
	b.cursor = nextStart + min(column, nextEnd-nextStart)
}

func renderEditor(writer io.Writer, buffer *editBuffer) {
	lines := strings.Split(string(buffer.text), "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	row, column := 0, 0
	for index, value := range buffer.text[:buffer.cursor] {
		_ = index
		if value == '\n' {
			row++
			column = 0
		} else {
			column++
		}
	}
	fmt.Fprint(writer, "\x1b[H\x1b[2JHorizon — Enter: отправить · Shift+Enter/Ctrl+J: новая строка · Ctrl+C: отменить\r\n\r\n")
	for index, line := range lines {
		if index != 0 {
			fmt.Fprint(writer, "\r\n")
		}
		fmt.Fprint(writer, line)
	}
	fmt.Fprintf(writer, "\x1b[%d;%dH", row+3, column+1)
}

func readKey(reader *bufio.Reader) (keyEvent, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return keyEvent{}, err
	}
	switch first {
	case 3:
		return keyEvent{kind: keyCancel}, nil
	case '\r':
		return keyEvent{kind: keySubmit}, nil
	case '\n':
		return keyEvent{kind: keyNewline}, nil
	case 8, 127:
		return keyEvent{kind: keyBackspace}, nil
	case 27:
		return readEscape(reader)
	}
	length := utf8.RuneLen(rune(first))
	if first >= utf8.RuneSelf {
		length = utf8ByteLength(first)
	}
	data := []byte{first}
	if length > 1 {
		rest := make([]byte, length-1)
		if _, err := io.ReadFull(reader, rest); err != nil {
			return keyEvent{}, err
		}
		data = append(data, rest...)
	}
	value, size := utf8.DecodeRune(data)
	if value == utf8.RuneError && size == 1 {
		return keyEvent{}, errors.New("terminal input is not valid UTF-8")
	}
	return keyEvent{kind: keyText, text: []rune{value}}, nil
}

func readEscape(reader *bufio.Reader) (keyEvent, error) {
	next, err := reader.ReadByte()
	if err != nil {
		return keyEvent{kind: keyUnknown}, nil
	}
	if next != '[' {
		return keyEvent{kind: keyUnknown}, nil
	}
	sequence := []byte{27, '['}
	for len(sequence) < 128 {
		value, err := reader.ReadByte()
		if err != nil {
			return keyEvent{}, err
		}
		sequence = append(sequence, value)
		if value >= 0x40 && value <= 0x7e {
			break
		}
	}
	text := string(sequence)
	if text == "\x1b[200~" {
		pasted, err := readPaste(reader)
		return keyEvent{kind: keyText, text: []rune(pasted)}, err
	}
	switch sequence[len(sequence)-1] {
	case 'A':
		return keyEvent{kind: keyUp}, nil
	case 'B':
		return keyEvent{kind: keyDown}, nil
	case 'C':
		return keyEvent{kind: keyRight}, nil
	case 'D':
		return keyEvent{kind: keyLeft}, nil
	case '~':
		if text == "\x1b[3~" {
			return keyEvent{kind: keyDelete}, nil
		}
		if strings.HasPrefix(text, "\x1b[27;2;13") {
			return keyEvent{kind: keyNewline}, nil
		}
	case 'u':
		return parseCSIU(strings.TrimSuffix(strings.TrimPrefix(text, "\x1b["), "u"))
	}
	return keyEvent{kind: keyUnknown}, nil
}

func parseCSIU(body string) (keyEvent, error) {
	parts := strings.Split(body, ";")
	codeText := strings.Split(parts[0], ":")[0]
	code, err := strconv.Atoi(codeText)
	if err != nil {
		return keyEvent{}, fmt.Errorf("invalid CSI-u key code %q", codeText)
	}
	modifier := 1
	if len(parts) > 1 {
		modifierText := strings.Split(parts[1], ":")[0]
		if modifierText != "" {
			modifier, err = strconv.Atoi(modifierText)
			if err != nil {
				return keyEvent{}, fmt.Errorf("invalid CSI-u modifier %q", modifierText)
			}
		}
	}
	actualModifiers := modifier - 1
	if code == 99 && actualModifiers&4 != 0 {
		return keyEvent{kind: keyCancel}, nil
	}
	if code == 13 {
		if actualModifiers&1 != 0 {
			return keyEvent{kind: keyNewline}, nil
		}
		return keyEvent{kind: keySubmit}, nil
	}
	if code == 127 || code == 8 {
		return keyEvent{kind: keyBackspace}, nil
	}
	if len(parts) > 2 && parts[2] != "" {
		var text []rune
		for _, encoded := range strings.Split(parts[2], ":") {
			value, err := strconv.Atoi(encoded)
			if err != nil || !utf8.ValidRune(rune(value)) {
				return keyEvent{}, fmt.Errorf("invalid CSI-u text code %q", encoded)
			}
			text = append(text, rune(value))
		}
		return keyEvent{kind: keyText, text: text}, nil
	}
	value := rune(code)
	if actualModifiers&1 != 0 && value >= 'a' && value <= 'z' {
		value -= 'a' - 'A'
	}
	if !utf8.ValidRune(value) {
		return keyEvent{}, fmt.Errorf("invalid CSI-u rune %d", code)
	}
	return keyEvent{kind: keyText, text: []rune{value}}, nil
}

func readPaste(reader *bufio.Reader) (string, error) {
	const end = "\x1b[201~"
	var data []byte
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		data = append(data, value)
		if len(data) >= len(end) && string(data[len(data)-len(end):]) == end {
			data = data[:len(data)-len(end)]
			break
		}
	}
	if !utf8.Valid(data) {
		return "", errors.New("pasted terminal input is not valid UTF-8")
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"), nil
}

func utf8ByteLength(first byte) int {
	switch {
	case first&0xe0 == 0xc0:
		return 2
	case first&0xf0 == 0xe0:
		return 3
	case first&0xf8 == 0xf0:
		return 4
	default:
		return 1
	}
}
