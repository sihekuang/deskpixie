package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxExpression: longer input is refused; a spoken sum is never this long.
const maxExpression = 200

// Calculate is `calculate`: arithmetic and unit conversion. Speech models are poor at mental math.
type Calculate struct{}

func (Calculate) Name() string { return "calculate" }

func (Calculate) Description() string {
	return "Exact arithmetic and unit conversion. Always use it for math instead of working it out yourself. " +
		"Pass either `expression`, or `value` with `from` and `to` units."
}

func (Calculate) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "expression": {
      "type": "string",
      "description": "Arithmetic with + - * / ^ % and parentheses, plus sqrt, abs, round, floor, ceil, min, max, ln, log10, sin, cos, tan, pi. Write percentages as multiplication (18% of 64 is 0.18 * 64) and numbers without thousands separators."
    },
    "value": { "type": "number", "description": "The amount to convert." },
    "from": { "type": "string", "description": "Unit to convert from, e.g. miles, kg, °F, cups, mph." },
    "to": { "type": "string", "description": "Unit to convert to." }
  },
  "additionalProperties": false
}`)
}

func (Calculate) Invoke(_ context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	a := parseArgs(raw)
	if expr, ok := a.text("expression"); ok {
		x, err := Evaluate(expr)
		if err != nil {
			return fail(err.Error())
		}
		return reply(map[string]any{"result": Spoken(x)})
	}
	value, okV := a.float("value")
	from, okF := a.text("from")
	to, okT := a.text("to")
	if !okV || !okF || !okT {
		return fail("pass an expression, or value with from and to")
	}
	x, fromName, toName, err := Convert(value, from, to)
	if err != nil {
		return fail(err.Error())
	}
	av, bv := Spoken(value), Spoken(x)
	return reply(map[string]any{"result": fmt.Sprintf("%s %s is %s %s", av, named(av, fromName), bv, named(bv, toName))})
}

// allowed lists the characters an arithmetic expression needs. Anything else (quotes, `=`, `;`)
// is refused up front, so strings, assignment and chaining can never be reached.
func allowed(c rune) bool {
	return (c < 128 && (c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')) ||
		strings.ContainsRune(" \t.,+-*/^%()_:×÷−", c)
}

// Evaluate computes an arithmetic expression with evalexpr-compatible semantics: every number is
// a float (7 / 2 is 3.5), ^ is a power, % is a float remainder, math functions may be written
// bare or as math::name, and the result must be finite.
//
// Differences from evalexpr, none exercised by the Rust tests: ^ is right-associative
// (2^3^2 = 512, the usual maths reading) and a unary minus binds looser than ^ (-2^2 = -4).
func Evaluate(expr string) (float64, error) {
	if len(expr) > maxExpression {
		return 0, errors.New("expression too long")
	}
	for _, c := range expr {
		if !allowed(c) {
			return 0, fmt.Errorf("can't evaluate: \"%c\" isn't arithmetic", c)
		}
	}
	p := &parser{toks: lex(expr)}
	x, err := p.parseExpr(0)
	if err == nil && p.peek().kind != tEOF {
		err = fmt.Errorf("unexpected %s", p.peek())
	}
	if err != nil {
		return 0, fmt.Errorf("can't evaluate: %w", err)
	}
	return finite(x)
}

func finite(x float64) (float64, error) {
	switch {
	case math.IsNaN(x):
		return 0, errors.New("the result is not a real number")
	case math.IsInf(x, 0):
		return 0, errors.New("the result is infinite (division by zero?)")
	}
	return x, nil
}

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tIdent
	tOp
	tBad
)

type token struct {
	kind tokKind
	text string
	num  float64
}

func (t token) String() string {
	if t.kind == tEOF {
		return "end of expression"
	}
	return "\"" + t.text + "\""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lex(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case isDigit(c) || (c == '.' && i+1 < len(s) && isDigit(s[i+1])):
			start := i
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			if i < len(s) && s[i] == '.' {
				i++
				for i < len(s) && isDigit(s[i]) {
					i++
				}
			}
			if i+1 < len(s) && (s[i] == 'e' || s[i] == 'E') && (isDigit(s[i+1]) || s[i+1] == '-' || s[i+1] == '+') {
				i += 2
				for i < len(s) && isDigit(s[i]) {
					i++
				}
			}
			text := s[start:i]
			n, err := strconv.ParseFloat(text, 64)
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				out = append(out, token{kind: tBad, text: text})
			} else {
				out = append(out, token{kind: tNum, text: text, num: n})
			}
		case c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			start := i
			for i < len(s) && (s[i] == '_' || s[i] == ':' || isDigit(s[i]) || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
				i++
			}
			out = append(out, token{kind: tIdent, text: s[start:i]})
		default:
			// Operators; the typographic ones are ASCII-fied (they are multi-byte in UTF-8).
			rest := s[i:]
			switch {
			case strings.HasPrefix(rest, "×"):
				out = append(out, token{kind: tOp, text: "*"})
				i += len("×")
			case strings.HasPrefix(rest, "÷"):
				out = append(out, token{kind: tOp, text: "/"})
				i += len("÷")
			case strings.HasPrefix(rest, "−"):
				out = append(out, token{kind: tOp, text: "-"})
				i += len("−")
			default:
				out = append(out, token{kind: tOp, text: string(c)})
				i++
			}
		}
	}
	return append(out, token{kind: tEOF})
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

// Binding powers: + - 95, * / % 100, ^ 120 (right-associative).
func binding(op string) (left, right int, ok bool) {
	switch op {
	case "+", "-":
		return 95, 96, true
	case "*", "/", "%":
		return 100, 101, true
	case "^":
		return 120, 120, true
	}
	return 0, 0, false
}

func (p *parser) parseExpr(minBP int) (float64, error) {
	lhs, err := p.parsePrefix()
	if err != nil {
		return 0, err
	}
	for {
		t := p.peek()
		if t.kind != tOp {
			return lhs, nil
		}
		l, r, ok := binding(t.text)
		if !ok || l < minBP {
			return lhs, nil
		}
		p.next()
		rhs, err := p.parseExpr(r)
		if err != nil {
			return 0, err
		}
		switch t.text {
		case "+":
			lhs += rhs
		case "-":
			lhs -= rhs
		case "*":
			lhs *= rhs
		case "/":
			lhs /= rhs
		case "%":
			lhs = math.Mod(lhs, rhs)
		case "^":
			lhs = math.Pow(lhs, rhs)
		}
	}
}

func (p *parser) parsePrefix() (float64, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		return t.num, nil
	case tIdent:
		if p.isOp("(") {
			p.next()
			return p.call(t.text)
		}
		return constant(t.text)
	case tOp:
		switch t.text {
		case "-":
			// Binds looser than ^ and tighter than * (evalexpr: 110 between 100 and 120).
			x, err := p.parseExpr(110)
			return -x, err
		case "(":
			x, err := p.parseExpr(0)
			if err != nil {
				return 0, err
			}
			if !p.isOp(")") {
				return 0, fmt.Errorf("expected \")\", found %s", p.peek())
			}
			p.next()
			return x, nil
		}
	}
	return 0, fmt.Errorf("unexpected %s", t)
}

func constant(name string) (float64, error) {
	switch name {
	case "pi":
		return math.Pi, nil
	case "e":
		return math.E, nil
	}
	return 0, fmt.Errorf("variable \"%s\" is not defined", name)
}

func (p *parser) call(name string) (float64, error) {
	var args []float64
	if !p.isOp(")") {
		for {
			x, err := p.parseExpr(0)
			if err != nil {
				return 0, err
			}
			args = append(args, x)
			if p.isOp(",") {
				p.next()
				continue
			}
			break
		}
	}
	if !p.isOp(")") {
		return 0, fmt.Errorf("expected \")\", found %s", p.peek())
	}
	p.next()
	return apply(name, args)
}

func apply(name string, a []float64) (float64, error) {
	name = strings.TrimPrefix(name, "math::")
	one := map[string]func(float64) float64{
		"sqrt": math.Sqrt, "cbrt": math.Cbrt, "abs": math.Abs, "ln": math.Log, "log2": math.Log2,
		"log10": math.Log10, "exp": math.Exp, "sin": math.Sin, "cos": math.Cos, "tan": math.Tan,
		"asin": math.Asin, "acos": math.Acos, "atan": math.Atan,
		"round": math.Round, "floor": math.Floor, "ceil": math.Ceil,
	}
	two := map[string]func(x, y float64) float64{"pow": math.Pow, "hypot": math.Hypot, "atan2": math.Atan2}
	if f, ok := one[name]; ok {
		if len(a) != 1 {
			return 0, fmt.Errorf("%s expects 1 argument, got %d", name, len(a))
		}
		return f(a[0]), nil
	}
	if f, ok := two[name]; ok {
		if len(a) != 2 {
			return 0, fmt.Errorf("%s expects 2 arguments, got %d", name, len(a))
		}
		return f(a[0], a[1]), nil
	}
	if name == "min" || name == "max" {
		if len(a) == 0 {
			return 0, fmt.Errorf("%s expects at least 1 argument", name)
		}
		best := a[0]
		for _, x := range a[1:] {
			if (name == "min" && x < best) || (name == "max" && x > best) {
				best = x
			}
		}
		return best, nil
	}
	return 0, fmt.Errorf("function \"%s\" is not defined", name)
}

// Spoken formats a number with up to six significant digits, no trailing zeros, no "-0".
func Spoken(x float64) string {
	if x == 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return "0"
	}
	digits := 5 - int(math.Floor(math.Log10(math.Abs(x))))
	digits = max(0, min(10, digits))
	s := strconv.FormatFloat(x, 'f', digits, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "-0" {
		return "0"
	}
	return s
}
