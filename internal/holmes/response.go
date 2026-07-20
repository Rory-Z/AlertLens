package holmes

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxJSONDepth = 1000

type responseDecoder struct {
	reader           *bufio.Reader
	analysis         string
	analysisTooLarge bool
}

func decodeResponse(r io.Reader) (string, bool, error) {
	decoder := responseDecoder{reader: bufio.NewReader(r)}
	err := decoder.decode()
	return decoder.analysis, decoder.analysisTooLarge, err
}

func (d *responseDecoder) decode() error {
	if err := d.expect('{'); err != nil {
		return fmt.Errorf("expected JSON object: %w", err)
	}
	if next, err := d.peekNonSpace(); err == nil && next == '}' {
		_, _ = d.takeNonSpace()
		return d.expectEOF()
	}
	for {
		name, nameTooLong, err := d.readString(len("analysis"))
		if err != nil {
			return err
		}
		if err := d.expect(':'); err != nil {
			return err
		}
		if name == "analysis" && !nameTooLong {
			analysis, tooLarge, err := d.readString(maxAnalysisBytes)
			if err != nil {
				return err
			}
			d.analysis, d.analysisTooLarge = analysis, tooLarge
		} else if err := d.skipValue(1); err != nil {
			return err
		}
		delimiter, err := d.takeNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case '}':
			return d.expectEOF()
		case ',':
		default:
			return fmt.Errorf("expected ',' or '}', got %q", delimiter)
		}
	}
}

func (d *responseDecoder) skipValue(depth int) error {
	if depth > maxJSONDepth {
		return errors.New("JSON nesting exceeds 1000 levels")
	}
	first, err := d.takeNonSpace()
	if err != nil {
		return err
	}
	switch first {
	case '{':
		return d.skipObject(depth)
	case '[':
		return d.skipArray(depth)
	case '"':
		_, _, err = d.readStringContent(0)
		return err
	case 't':
		return d.expectLiteral("rue")
	case 'f':
		return d.expectLiteral("alse")
	case 'n':
		return d.expectLiteral("ull")
	case '-':
		return d.skipNumber(first)
	default:
		if first >= '0' && first <= '9' {
			return d.skipNumber(first)
		}
		return fmt.Errorf("unexpected JSON value %q", first)
	}
}

func (d *responseDecoder) skipObject(depth int) error {
	if next, err := d.peekNonSpace(); err == nil && next == '}' {
		_, _ = d.takeNonSpace()
		return nil
	}
	for {
		if _, _, err := d.readString(0); err != nil {
			return err
		}
		if err := d.expect(':'); err != nil {
			return err
		}
		if err := d.skipValue(depth + 1); err != nil {
			return err
		}
		delimiter, err := d.takeNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case '}':
			return nil
		case ',':
		default:
			return fmt.Errorf("expected ',' or '}', got %q", delimiter)
		}
	}
}

func (d *responseDecoder) skipArray(depth int) error {
	if next, err := d.peekNonSpace(); err == nil && next == ']' {
		_, _ = d.takeNonSpace()
		return nil
	}
	for {
		if err := d.skipValue(depth + 1); err != nil {
			return err
		}
		delimiter, err := d.takeNonSpace()
		if err != nil {
			return err
		}
		switch delimiter {
		case ']':
			return nil
		case ',':
		default:
			return fmt.Errorf("expected ',' or ']', got %q", delimiter)
		}
	}
}

func (d *responseDecoder) readString(limit int) (string, bool, error) {
	if err := d.expect('"'); err != nil {
		return "", false, err
	}
	return d.readStringContent(limit)
}

func (d *responseDecoder) readStringContent(limit int) (string, bool, error) {
	var value strings.Builder
	tooLarge := false
	for {
		r, _, err := d.reader.ReadRune()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", tooLarge, err
		}
		switch r {
		case '"':
			return value.String(), tooLarge, nil
		case '\\':
			r, err = d.readEscape()
			if err != nil {
				return "", tooLarge, err
			}
		default:
			if r < 0x20 {
				return "", tooLarge, errors.New("unescaped control character in string")
			}
		}
		size := utf8.RuneLen(r)
		if size < 0 {
			r, size = utf8.RuneError, utf8.RuneLen(utf8.RuneError)
		}
		if value.Len()+size <= limit {
			_, _ = value.WriteRune(r)
		} else {
			tooLarge = true
		}
	}
}

func (d *responseDecoder) readEscape() (rune, error) {
	escape, err := d.reader.ReadByte()
	if err != nil {
		return 0, err
	}
	switch escape {
	case '"', '\\', '/':
		return rune(escape), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		first, err := d.readHexRune()
		if err != nil {
			return 0, err
		}
		if first < 0xd800 || first > 0xdbff {
			if first >= 0xdc00 && first <= 0xdfff {
				return utf8.RuneError, nil
			}
			return first, nil
		}
		next, err := d.reader.Peek(6)
		if err == nil && next[0] == '\\' && next[1] == 'u' {
			second, ok := parseHexRune(next[2:])
			if ok && second >= 0xdc00 && second <= 0xdfff {
				_, _ = d.reader.Discard(6)
				return utf16.DecodeRune(first, second), nil
			}
		}
		return utf8.RuneError, nil
	default:
		return 0, fmt.Errorf("invalid string escape %q", escape)
	}
}

func (d *responseDecoder) readHexRune() (rune, error) {
	var encoded [4]byte
	if _, err := io.ReadFull(d.reader, encoded[:]); err != nil {
		return 0, err
	}
	r, ok := parseHexRune(encoded[:])
	if !ok {
		return 0, errors.New("invalid Unicode escape")
	}
	return r, nil
}

func parseHexRune(encoded []byte) (rune, bool) {
	var value rune
	for _, b := range encoded[:4] {
		value <<= 4
		switch {
		case b >= '0' && b <= '9':
			value += rune(b - '0')
		case b >= 'a' && b <= 'f':
			value += rune(b-'a') + 10
		case b >= 'A' && b <= 'F':
			value += rune(b-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func (d *responseDecoder) skipNumber(first byte) error {
	if first == '-' {
		var err error
		first, err = d.takeByte()
		if err != nil {
			return err
		}
	}
	if first == '0' {
		if next, err := d.peekByte(); err == nil && next >= '0' && next <= '9' {
			return errors.New("leading zero in JSON number")
		}
	} else if first >= '1' && first <= '9' {
		d.takeDigits()
	} else {
		return errors.New("invalid JSON number")
	}
	if next, err := d.peekByte(); err == nil && next == '.' {
		_, _ = d.takeByte()
		if err := d.takeRequiredDigit(); err != nil {
			return err
		}
		d.takeDigits()
	}
	if next, err := d.peekByte(); err == nil && (next == 'e' || next == 'E') {
		_, _ = d.takeByte()
		if next, err = d.peekByte(); err == nil && (next == '+' || next == '-') {
			_, _ = d.takeByte()
		}
		if err := d.takeRequiredDigit(); err != nil {
			return err
		}
		d.takeDigits()
	}
	if next, err := d.peekByte(); err == nil && !isJSONDelimiter(next) {
		return fmt.Errorf("invalid character %q after JSON number", next)
	}
	return nil
}

func (d *responseDecoder) takeRequiredDigit() error {
	digit, err := d.takeByte()
	if err != nil {
		return err
	}
	if digit < '0' || digit > '9' {
		return errors.New("JSON number requires a digit")
	}
	return nil
}

func (d *responseDecoder) takeDigits() {
	for {
		next, err := d.peekByte()
		if err != nil || next < '0' || next > '9' {
			return
		}
		_, _ = d.takeByte()
	}
}

func (d *responseDecoder) expectLiteral(rest string) error {
	for i := range len(rest) {
		got, err := d.reader.ReadByte()
		if err != nil {
			return err
		}
		if got != rest[i] {
			return errors.New("invalid JSON literal")
		}
	}
	return nil
}

func (d *responseDecoder) expect(want byte) error {
	got, err := d.takeNonSpace()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("expected %q, got %q", want, got)
	}
	return nil
}

func (d *responseDecoder) expectEOF() error {
	if got, err := d.peekNonSpace(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected data after JSON object: %q", got)
	}
	return nil
}

func (d *responseDecoder) takeNonSpace() (byte, error) {
	next, err := d.peekNonSpace()
	if err != nil {
		return 0, err
	}
	_, _ = d.reader.Discard(1)
	return next, nil
}

func (d *responseDecoder) peekNonSpace() (byte, error) {
	for {
		next, err := d.peekByte()
		if err != nil {
			return 0, err
		}
		if !isJSONSpace(next) {
			return next, nil
		}
		_, _ = d.reader.Discard(1)
	}
}

func (d *responseDecoder) takeByte() (byte, error) { return d.reader.ReadByte() }

func (d *responseDecoder) peekByte() (byte, error) {
	next, err := d.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return next[0], nil
}

func isJSONSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

func isJSONDelimiter(b byte) bool {
	return isJSONSpace(b) || b == ',' || b == ']' || b == '}'
}
