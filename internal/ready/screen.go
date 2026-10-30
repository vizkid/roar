package ready

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// cell is one printed character, whether it was drawn dim (SGR 2), and
// its foreground colour as SGR parameters ("" for the default). Claude and
// Codex draw placeholder text dim; Devin draws it in one grey.
type cell struct {
	r   rune
	dim bool
	fg  string
}

// attrs are the SGR attributes the parser carries from cell to cell.
type attrs struct {
	dim bool
	fg  string
}

type row struct{ cells []cell }

func (r row) text() string {
	var b strings.Builder
	for _, c := range r.cells {
		b.WriteRune(c.r)
	}
	return b.String()
}

// screen is the visible screen from Rex's `format` VT output.
type screen struct {
	rows                 []row
	cursorRow, cursorCol int // 1-based, as Rex reports them
}

var errNoCursor = errors.New("no cursor position in VT output")

// Rex appends the cursor as a final CUP sequence when extra_cursor is set.
var finalCUP = regexp.MustCompile("\x1b\\[(\\d+);(\\d+)H$")

func parseScreen(vt string, rows int) (screen, error) {
	m := finalCUP.FindStringSubmatchIndex(vt)
	if m == nil {
		return screen{}, errNoCursor
	}
	var sc screen
	sc.cursorRow, _ = strconv.Atoi(vt[m[2]:m[3]])
	sc.cursorCol, _ = strconv.Atoi(vt[m[4]:m[5]])
	lines := strings.Split(vt[:m[0]], "\r\n")
	// Primary-screen output carries scrollback above the screen.
	if rows > 0 && len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	var at attrs
	for _, l := range lines {
		var r row
		r.cells, at = parseLine(l, at)
		sc.rows = append(sc.rows, r)
	}
	return sc, nil
}

// parseLine interprets the few sequences Rex emits in VT output: SGR for
// attributes and CUF for runs of blank cells. Other sequences are skipped.
func parseLine(l string, at attrs) ([]cell, attrs) {
	var cells []cell
	rs := []rune(l)
	for i := 0; i < len(rs); i++ {
		if rs[i] != 0x1b {
			cells = append(cells, cell{rs[i], at.dim, at.fg})
			continue
		}
		if i+1 >= len(rs) {
			break
		}
		switch rs[i+1] {
		case '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j >= len(rs) {
				return cells, at
			}
			params := string(rs[i+2 : j])
			switch rs[j] {
			case 'm':
				at = applySGR(params, at)
			case 'C':
				n, err := strconv.Atoi(params)
				if err != nil || n < 1 {
					n = 1
				}
				for range n {
					cells = append(cells, cell{' ', false, ""})
				}
			}
			i = j
		case ']': // OSC, ended by BEL or ESC \
			j := i + 2
			for j < len(rs) && rs[j] != 0x07 && !(rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\') {
				j++
			}
			if j < len(rs) && rs[j] == 0x1b {
				j++
			}
			i = j
		default:
			i++
		}
	}
	return cells, at
}

func applySGR(params string, at attrs) attrs {
	if params == "" {
		return attrs{}
	}
	parts := strings.Split(params, ";")
	for k := 0; k < len(parts); k++ {
		switch p := parts[k]; p {
		case "0":
			at = attrs{}
		case "2":
			at.dim = true
		case "22":
			at.dim = false
		case "39":
			at.fg = ""
		case "38", "48", "58":
			// Extended colour: skip its arguments so "2" in "38;2;r;g;b" is
			// not read as dim. Keep the foreground's.
			n := 0
			if k+1 < len(parts) && parts[k+1] == "5" {
				n = 2
			} else if k+1 < len(parts) && parts[k+1] == "2" {
				n = 4
			}
			if p == "38" && k+n < len(parts) {
				at.fg = strings.Join(parts[k+1:k+1+n], ";")
			}
			k += n
		default:
			if len(p) == 2 && (p[0] == '3' || p[0] == '9') && p[1] >= '0' && p[1] <= '7' {
				at.fg = p
			}
		}
	}
	return at
}

// isRule reports a horizontal rule row (Claude draws them around its composer).
func (r row) isRule() bool {
	n := 0
	for _, c := range r.cells {
		switch c.r {
		case '─', '━':
			n++
		case ' ', ' ':
		default:
			return false
		}
	}
	return n >= 8
}

// blank reports a row with no visible characters.
func (r row) blank() bool {
	return strings.TrimSpace(strings.ReplaceAll(r.text(), " ", " ")) == ""
}

// placeholderFG is the foreground an agent draws placeholder text in, for
// an agent that does not use dim. Another colour reads as typed text, so a
// changed theme makes Roar wait, never type over a draft.
var placeholderFG = map[string]string{"devin": "2;124;124;124"}

// typedText reports whether any cell from index start on is visible and
// not the agent's placeholder.
func (r row) typedText(start int, agent string) bool {
	ph := placeholderFG[agent]
	for i := start; i < len(r.cells); i++ {
		c := r.cells[i]
		if c.r != ' ' && c.r != ' ' && !c.dim && (ph == "" || c.fg != ph) {
			return true
		}
	}
	return false
}
