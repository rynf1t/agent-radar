package view

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// keyCode names the keys the viewer uses. Printable keys use their rune.
type keyCode rune

const (
	keyNone keyCode = -iota - 1
	keyUp
	keyDown
	keyHome
	keyEnd
	keyPageUp
	keyPageDown
	keyEnter
	keyEscape
	keyBackspace
	keyTab
)

// input is one decoded thing the terminal sent.
type input struct {
	key   keyCode
	ctrl  bool   // for letters: Control was held (ctrl+c arrives as 'c' with ctrl)
	osc   string // an OSC reply body, such as "11;rgb:1e1e/1f1f/2222"
	focus int    // 1 focus in, -1 focus out
	click *mouse
	wheel int // -1 up, 1 down
	paste string
}

type mouse struct{ col, row int }

// decoder turns terminal bytes into inputs. It keeps a partial sequence
// across reads.
type decoder struct {
	buf     []byte
	inPaste bool
	paste   strings.Builder
}

// feed decodes b. A lone ESC at the end of a read is the Escape key, since
// terminals send a whole sequence in one write.
func (d *decoder) feed(b []byte, emit func(input)) {
	d.buf = append(d.buf, b...)
	for len(d.buf) > 0 {
		n := d.one(emit)
		if n == 0 {
			if len(d.buf) == 1 && d.buf[0] == 0x1b {
				emit(input{key: keyEscape})
				d.buf = d.buf[:0]
			}
			return
		}
		d.buf = d.buf[n:]
	}
}

// one decodes the sequence at the front of buf and returns its length, or 0
// if it is incomplete.
func (d *decoder) one(emit func(input)) int {
	b := d.buf
	if d.inPaste {
		end := strings.Index(string(b), "\x1b[201~")
		if end < 0 {
			d.paste.Write(b)
			return len(b)
		}
		d.paste.Write(b[:end])
		emit(input{paste: d.paste.String()})
		d.paste.Reset()
		d.inPaste = false
		return end + 6
	}
	c := b[0]
	switch {
	case c == 0x1b:
		if len(b) < 2 {
			return 0
		}
		switch b[1] {
		case '[':
			return d.csi(b, emit)
		case 'O':
			if len(b) < 3 {
				return 0
			}
			switch b[2] {
			case 'A':
				emit(input{key: keyUp})
			case 'B':
				emit(input{key: keyDown})
			case 'H':
				emit(input{key: keyHome})
			case 'F':
				emit(input{key: keyEnd})
			}
			return 3
		case ']':
			for i := 2; i < len(b); i++ {
				if b[i] == 0x07 {
					emit(input{osc: string(b[2:i])})
					return i + 1
				}
				if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
					emit(input{osc: string(b[2:i])})
					return i + 2
				}
			}
			return 0
		case 0x1b:
			emit(input{key: keyEscape})
			return 1
		}
		// Alt+key: treat as the key.
		return 1
	case c == '\r' || c == '\n':
		emit(input{key: keyEnter})
		return 1
	case c == 0x7f || c == 0x08:
		emit(input{key: keyBackspace})
		return 1
	case c == '\t':
		emit(input{key: keyTab})
		return 1
	case c < 0x20:
		emit(input{key: keyCode('a' + c - 1), ctrl: true})
		return 1
	}
	if !utf8.FullRune(b) {
		return 0
	}
	r, n := utf8.DecodeRune(b)
	emit(input{key: keyCode(r)})
	return n
}

func (d *decoder) csi(b []byte, emit func(input)) int {
	i := 2
	for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
		i++
	}
	if i >= len(b) {
		return 0
	}
	params, final := string(b[2:i]), b[i]
	n := i + 1
	switch final {
	case 'A':
		emit(input{key: keyUp})
	case 'B':
		emit(input{key: keyDown})
	case 'H':
		emit(input{key: keyHome})
	case 'F':
		emit(input{key: keyEnd})
	case 'I':
		emit(input{focus: 1})
	case 'O':
		emit(input{focus: -1})
	case '~':
		switch params {
		case "1", "7":
			emit(input{key: keyHome})
		case "4", "8":
			emit(input{key: keyEnd})
		case "5":
			emit(input{key: keyPageUp})
		case "6":
			emit(input{key: keyPageDown})
		case "200":
			d.inPaste = true
		}
	case 'M', 'm':
		if strings.HasPrefix(params, "<") && final == 'M' {
			f := strings.Split(params[1:], ";")
			if len(f) == 3 {
				btn, _ := strconv.Atoi(f[0])
				col, _ := strconv.Atoi(f[1])
				row, _ := strconv.Atoi(f[2])
				switch btn {
				case 0:
					emit(input{click: &mouse{col, row}})
				case 64:
					emit(input{wheel: -1})
				case 65:
					emit(input{wheel: 1})
				}
			}
		}
	}
	return n
}
