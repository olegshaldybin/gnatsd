// Copyright 2013 Apcera Inc. All rights reserved.

// Customized heavily from
// https://github.com/BurntSushi/toml/blob/master/lex.go, which is based on
// Rob Pike's talk: http://cuddle.googlecode.com/hg/talk/lex.html

// The format supported is less restrictive than today's formats.
// Supports mixed Arrays [], nexted Maps {}, multiple comment types (# and //)
// Also supports key value assigments using '=' or ':' or whiteSpace()
//   e.g. foo = 2, foo : 2, foo 2
// maps can be assigned with no key separator as well
// semicolons as value terminators in key/value assignments are optional
//
// see lex_test.go for more examples.

package conf

import (
	"fmt"
	"unicode/utf8"
)

type itemType int

const (
	itemError itemType = iota
	itemNIL            // used in the parser to indicate no type
	itemEOF
	itemKey
	itemText
	itemString
	itemBool
	itemInteger
	itemFloat
	itemDatetime
	itemArrayStart
	itemArrayEnd
	itemMapStart
	itemMapEnd
	itemCommentStart
)

const (
	eof               = 0
	mapStart          = '{'
	mapEnd            = '}'
	keySepEqual       = '='
	keySepColon       = ':'
	arrayStart        = '['
	arrayEnd          = ']'
	arrayValTerm      = ','
	mapValTerm        = ','
	commentHashStart  = '#'
	commentSlashStart = '/'
	dqStringStart     = '"'
	dqStringEnd       = '"'
	sqStringStart     = '\''
	sqStringEnd       = '\''
	optValTerm        = ';'
)

type stateFn func(lx *lexer) stateFn

type lexer struct {
	input string
	start int
	pos   int
	width int
	line  int
	state stateFn
	items chan item

	// A stack of state functions used to maintain context.
	// The idea is to reuse parts of the state machine in various places.
	// For example, values can appear at the top level or within arbitrarily
	// nested arrays. The last state on the stack is used after a value has
	// been lexed. Similarly for comments.
	stack []stateFn
}

type item struct {
	typ  itemType
	val  string
	line int
}

func (lx *lexer) nextItem() item {
	for {
		select {
		case item := <-lx.items:
			return item
		default:
			lx.state = lx.state(lx)
		}
	}
	panic("not reached")
}

func lex(input string) *lexer {
	lx := &lexer{
		input: input,
		state: lexTop,
		line:  1,
		items: make(chan item, 10),
		stack: make([]stateFn, 0, 10),
	}
	return lx
}

func (lx *lexer) push(state stateFn) {
	lx.stack = append(lx.stack, state)
}

func (lx *lexer) pop() stateFn {
	if len(lx.stack) == 0 {
		return lx.errorf("BUG in lexer: no states to pop.")
	}
	li := len(lx.stack) - 1
	last := lx.stack[li]
	lx.stack = lx.stack[0:li]
	return last
}

func (lx *lexer) emit(typ itemType) {
	lx.items <- item{typ, lx.input[lx.start:lx.pos], lx.line}
	lx.start = lx.pos
}

func (lx *lexer) next() (r rune) {
	if lx.pos >= len(lx.input) {
		lx.width = 0
		return eof
	}

	if lx.input[lx.pos] == '\n' {
		lx.line++
	}
	r, lx.width = utf8.DecodeRuneInString(lx.input[lx.pos:])
	lx.pos += lx.width
	return r
}

// ignore skips over the pending input before this point.
func (lx *lexer) ignore() {
	lx.start = lx.pos
}

// backup steps back one rune. Can be called only once per call of next.
func (lx *lexer) backup() {
	lx.pos -= lx.width
	if lx.pos < len(lx.input) && lx.input[lx.pos] == '\n' {
		lx.line--
	}
}

// accept consumes the next rune if it's equal to `valid`.
func (lx *lexer) accept(valid rune) bool {
	if lx.next() == valid {
		return true
	}
	lx.backup()
	return false
}

// peek returns but does not consume the next rune in the input.
func (lx *lexer) peek() rune {
	r := lx.next()
	lx.backup()
	return r
}

// errorf stops all lexing by emitting an error and returning `nil`.
// Note that any value that is a character is escaped if it's a special
// character (new lines, tabs, etc.).
func (lx *lexer) errorf(format string, values ...interface{}) stateFn {
	for i, value := range values {
		if v, ok := value.(rune); ok {
			values[i] = escapeSpecial(v)
		}
	}
	lx.items <- item{
		itemError,
		fmt.Sprintf(format, values...),
		lx.line,
	}
	return nil
}

// lexTop consumes elements at the top level of TOML data.
func lexTop(lx *lexer) stateFn {
	r := lx.next()
	if isWhitespace(r) || isNL(r) {
		return lexSkip(lx, lexTop)
	}

	switch r {
	case commentHashStart:
		lx.push(lexTop)
		return lexCommentStart
	case commentSlashStart:
		rn := lx.next()
		if rn == commentSlashStart {
			lx.push(lexTop)
			return lexCommentStart
		}
		lx.backup()
		fallthrough
	case eof:
		if lx.pos > lx.start {
			return lx.errorf("Unexpected EOF.")
		}
		lx.emit(itemEOF)
		return nil
	}

	// At this point, the only valid item can be a key, so we back up
	// and let the key lexer do the rest.
	lx.backup()
	lx.push(lexTopValueEnd)
	return lexKeyStart
}

// lexTopValueEnd is entered whenever a top-level value has been consumed.
// It must see only whitespace, and will turn back to lexTop upon a new line.
// If it sees EOF, it will quit the lexer successfully.
func lexTopValueEnd(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case r == commentHashStart:
		// a comment will read to a new line for us.
		lx.push(lexTop)
		return lexCommentStart
	case r == commentSlashStart:
		rn := lx.next()
		if rn == commentSlashStart {
			lx.push(lexTop)
			return lexCommentStart
		}
		lx.backup()
		fallthrough
	case isWhitespace(r):
		return lexTopValueEnd
	case isNL(r) || r == eof || r == optValTerm:
		lx.ignore()
		return lexTop
	}
	return lx.errorf("Expected a top-level value to end with a new line, "+
		"comment or EOF, but got '%s' instead.", r)
}

// lexKeyStart consumes a key name up until the first non-whitespace character.
// lexKeyStart will ignore whitespace.
func lexKeyStart(lx *lexer) stateFn {
	r := lx.peek()
	switch {
	case isKeySeparator(r):
		return lx.errorf("Unexpected key separator '%s'.", r)
	case isWhitespace(r) || isNL(r):
		lx.next()
		return lexSkip(lx, lexKeyStart)
	}
	lx.ignore()
	lx.next()
	return lexKey
}

// lexKey consumes the text of a key. Assumes that the first character (which
// is not whitespace) has already been consumed.
func lexKey(lx *lexer) stateFn {
	r := lx.peek()
	if isWhitespace(r) || isNL(r) || isKeySeparator(r) {
		lx.emit(itemKey)
		return lexKeyEnd
	}
	lx.next()
	return lexKey
}

// lexKeyEnd consumes the end of a key (up to the key separator).
// Assumes that the first whitespace character after a key (or the '=' or ':'
// separator) has NOT been consumed.
func lexKeyEnd(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r) || isNL(r):
		return lexSkip(lx, lexKeyEnd)
	case isKeySeparator(r):
		return lexSkip(lx, lexValue)
	}
	// We start the value here
	lx.backup()
	return lexValue
}

// lexValue starts the consumption of a value anywhere a value is expected.
// lexValue will ignore whitespace.
// After a value is lexed, the last state on the next is popped and returned.
func lexValue(lx *lexer) stateFn {
	// We allow whitespace to precede a value, but NOT new lines.
	// In array syntax, the array states are responsible for ignoring new lines.
	r := lx.next()
	if isWhitespace(r) {
		return lexSkip(lx, lexValue)
	}

	switch {
	case r == arrayStart:
		lx.ignore()
		lx.emit(itemArrayStart)
		return lexArrayValue
	case r == mapStart:
		lx.ignore()
		lx.emit(itemMapStart)
		return lexMapKeyStart
	case r == dqStringStart || r == sqStringStart:
		lx.ignore() // ignore the " or '
		return lexString
	case r == '-':
		return lexNumberStart
	case isDigit(r):
		lx.backup() // avoid an extra state and use the same as above
		return lexNumberOrDateStart
	case r == '.': // special error case, be kind to users
		return lx.errorf("Floats must start with a digit, not '.'.")
	case isNL(r):
		return lx.errorf("Expected value but found new line")
	}
	return lexString
	//return lx.errorf("Expected value but found '%s' instead.", r)
}

// lexArrayValue consumes one value in an array. It assumes that '[' or ','
// have already been consumed. All whitespace and new lines are ignored.
func lexArrayValue(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r) || isNL(r):
		return lexSkip(lx, lexArrayValue)
	case r == commentHashStart:
		lx.push(lexArrayValue)
		return lexCommentStart
	case r == commentSlashStart:
		rn := lx.next()
		if rn == commentSlashStart {
			lx.push(lexArrayValue)
			return lexCommentStart
		}
		lx.backup()
		fallthrough
	case r == arrayValTerm:
		return lx.errorf("Unexpected array value terminator '%s'.",
			arrayValTerm)
	case r == arrayEnd:
		return lexArrayEnd
	}

	lx.backup()
	lx.push(lexArrayValueEnd)
	return lexValue
}

// lexArrayValueEnd consumes the cruft between values of an array. Namely,
// it ignores whitespace and expects either a ',' or a ']'.
func lexArrayValueEnd(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r):
		return lexSkip(lx, lexArrayValueEnd)
	case r == commentHashStart:
		lx.push(lexArrayValueEnd)
		return lexCommentStart
	case r == commentSlashStart:
		rn := lx.next()
		if rn == commentSlashStart {
			lx.push(lexArrayValueEnd)
			return lexCommentStart
		}
		lx.backup()
		fallthrough
	case r == arrayValTerm || isNL(r):
		return lexSkip(lx, lexArrayValue) // Move onto next
	case r == arrayEnd:
		return lexArrayEnd
	}
	return lx.errorf("Expected an array value terminator '%s' or an array "+
		"terminator '%s', but got '%s' instead.", arrayValTerm, arrayEnd, r)
}

// lexArrayEnd finishes the lexing of an array. It assumes that a ']' has
// just been consumed.
func lexArrayEnd(lx *lexer) stateFn {
	lx.ignore()
	lx.emit(itemArrayEnd)
	return lx.pop()
}

// lexMapKeyStart consumes a key name up until the first non-whitespace
// character.
// lexMapKeyStart will ignore whitespace.
func lexMapKeyStart(lx *lexer) stateFn {
	r := lx.peek()
	switch {
	case isKeySeparator(r):
		return lx.errorf("Unexpected key separator '%s'.", r)
	case isWhitespace(r) || isNL(r):
		lx.next()
		return lexSkip(lx, lexMapKeyStart)
	case r == mapEnd:
		lx.next()
		return lexSkip(lx, lexMapEnd)
	}
	lx.ignore()
	lx.next()
	return lexMapKey
}

// lexKey consumes the text of a key. Assumes that the first character (which
// is not whitespace) has already been consumed.
func lexMapKey(lx *lexer) stateFn {
	r := lx.peek()
	if isWhitespace(r) || isNL(r) || isKeySeparator(r) {
		lx.emit(itemKey)
		return lexMapKeyEnd
	}
	lx.next()
	return lexMapKey
}

// lexMapKeyEnd consumes the end of a key (up to the key separator).
// Assumes that the first whitespace character after a key (or the '='
// separator) has NOT been consumed.
func lexMapKeyEnd(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r) || isNL(r):
		return lexSkip(lx, lexMapKeyEnd)
	case isKeySeparator(r):
		return lexSkip(lx, lexMapValue)
	}
	// We start the value here
	lx.backup()
	return lexMapValue
}

// lexMapValue consumes one value in a map. It assumes that '{' or ','
// have already been consumed. All whitespace and new lines are ignored.
// Map values can be separated by ',' or simple NLs.
func lexMapValue(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r) || isNL(r):
		return lexSkip(lx, lexMapValue)
	case r == commentHashStart:
		lx.push(lexMapValue)
		return lexCommentStart
	case r == commentSlashStart:
		rn := lx.next()
		if rn == commentSlashStart {
			lx.push(lexMapValue)
			return lexCommentStart
		}
		lx.backup()
		fallthrough
	case r == mapValTerm:
		return lx.errorf("Unexpected map value terminator '%s'.",
			mapValTerm)
	case r == mapEnd:
		return lexSkip(lx, lexMapEnd)
	}
	lx.backup()
	lx.push(lexMapValueEnd)
	return lexValue
}

// lexMapValueEnd consumes the cruft between values of a map. Namely,
// it ignores whitespace and expects either a ',' or a '}'.
func lexMapValueEnd(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r):
		return lexSkip(lx, lexMapValueEnd)
	case r == commentHashStart:
		lx.push(lexMapValueEnd)
		return lexCommentStart
	case r == commentSlashStart:
		rn := lx.next()
		if rn == commentSlashStart {
			lx.push(lexMapValueEnd)
			return lexCommentStart
		}
		lx.backup()
		fallthrough
	case r == optValTerm || r == mapValTerm || isNL(r):
		return lexSkip(lx, lexMapKeyStart) // Move onto next
	case r == mapEnd:
		return lexSkip(lx, lexMapEnd)
	}
	return lx.errorf("Expected a map value terminator '%s' or a map "+
		"terminator '%s', but got '%s' instead.", mapValTerm, mapEnd, r)
}

// lexMapEnd finishes the lexing of a map. It assumes that a '}' has
// just been consumed.
func lexMapEnd(lx *lexer) stateFn {
	lx.ignore()
	lx.emit(itemMapEnd)
	return lx.pop()
}

// Checks if the unquoted string was actually a boolean
func (lx *lexer) isBool() bool {
	str := lx.input[lx.start:lx.pos]
	return str == "true" || str == "false" || str == "TRUE" || str == "FALSE"
}

// lexString consumes the inner contents of a string. It assumes that the
// beginning '"' has already been consumed and ignored.
func lexString(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case r == '\\':
		return lexStringEscape
	// Termination of non-quoted strings
	case isNL(r) || r == eof || r == optValTerm || isWhitespace(r):
		lx.backup()
		if lx.isBool() {
			lx.emit(itemBool)
		} else {
			lx.emit(itemString)
		}
		return lx.pop()
	case r == dqStringEnd || r == sqStringEnd:
		lx.backup()
		lx.emit(itemString)
		lx.next()
		lx.ignore()
		return lx.pop()
	}
	return lexString
}

// lexStringEscape consumes an escaped character. It assumes that the preceding
// '\\' has already been consumed.
func lexStringEscape(lx *lexer) stateFn {
	r := lx.next()
	switch r {
	case 'x':
		return lexStringBinary
	case 't':
		fallthrough
	case 'n':
		fallthrough
	case 'r':
		fallthrough
	case '"':
		fallthrough
	case '\\':
		return lexString
	}
	return lx.errorf("Invalid escape character '%s'. Only the following "+
		"escape characters are allowed: \\xXX, \\t, \\n, \\r, \\\", \\\\.", r)
}

// lexStringBinary consumes two hexadecimal digits following '\x'. It assumes
// that the '\x' has already been consumed.
func lexStringBinary(lx *lexer) stateFn {
	r := lx.next()
	if !isHexadecimal(r) {
		return lx.errorf("Expected two hexadecimal digits after '\\x', but "+
			"got '%s' instead.", r)
	}

	r = lx.next()
	if !isHexadecimal(r) {
		return lx.errorf("Expected two hexadecimal digits after '\\x', but "+
			"got '%s' instead.", r)
	}
	return lexString
}

// lexNumberOrDateStart consumes either a (positive) integer, float or datetime.
// It assumes that NO negative sign has been consumed.
func lexNumberOrDateStart(lx *lexer) stateFn {
	r := lx.next()
	if !isDigit(r) {
		if r == '.' {
			return lx.errorf("Floats must start with a digit, not '.'.")
		} else {
			return lx.errorf("Expected a digit but got '%s'.", r)
		}
	}
	return lexNumberOrDate
}

// lexNumberOrDate consumes either a (positive) integer, float or datetime.
func lexNumberOrDate(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case r == '-':
		if lx.pos-lx.start != 5 {
			return lx.errorf("All ISO8601 dates must be in full Zulu form.")
		}
		return lexDateAfterYear
	case isDigit(r):
		return lexNumberOrDate
	case r == '.':
		return lexFloatStart
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexDateAfterYear consumes a full Zulu Datetime in ISO8601 format.
// It assumes that "YYYY-" has already been consumed.
func lexDateAfterYear(lx *lexer) stateFn {
	formats := []rune{
		// digits are '0'.
		// everything else is direct equality.
		'0', '0', '-', '0', '0',
		'T',
		'0', '0', ':', '0', '0', ':', '0', '0',
		'Z',
	}
	for _, f := range formats {
		r := lx.next()
		if f == '0' {
			if !isDigit(r) {
				return lx.errorf("Expected digit in ISO8601 datetime, "+
					"but found '%s' instead.", r)
			}
		} else if f != r {
			return lx.errorf("Expected '%s' in ISO8601 datetime, "+
				"but found '%s' instead.", f, r)
		}
	}
	lx.emit(itemDatetime)
	return lx.pop()
}

// lexNumberStart consumes either an integer or a float. It assumes that a
// negative sign has already been read, but that *no* digits have been consumed.
// lexNumberStart will move to the appropriate integer or float states.
func lexNumberStart(lx *lexer) stateFn {
	// we MUST see a digit. Even floats have to start with a digit.
	r := lx.next()
	if !isDigit(r) {
		if r == '.' {
			return lx.errorf("Floats must start with a digit, not '.'.")
		} else {
			return lx.errorf("Expected a digit but got '%s'.", r)
		}
	}
	return lexNumber
}

// lexNumber consumes an integer or a float after seeing the first digit.
func lexNumber(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isDigit(r):
		return lexNumber
	case r == '.':
		return lexFloatStart
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexFloatStart starts the consumption of digits of a float after a '.'.
// Namely, at least one digit is required.
func lexFloatStart(lx *lexer) stateFn {
	r := lx.next()
	if !isDigit(r) {
		return lx.errorf("Floats must have a digit after the '.', but got "+
			"'%s' instead.", r)
	}
	return lexFloat
}

// lexFloat consumes the digits of a float after a '.'.
// Assumes that one digit has been consumed after a '.' already.
func lexFloat(lx *lexer) stateFn {
	r := lx.next()
	if isDigit(r) {
		return lexFloat
	}

	lx.backup()
	lx.emit(itemFloat)
	return lx.pop()
}

// lexCommentStart begins the lexing of a comment. It will emit
// itemCommentStart and consume no characters, passing control to lexComment.
func lexCommentStart(lx *lexer) stateFn {
	lx.ignore()
	lx.emit(itemCommentStart)
	return lexComment
}

// lexComment lexes an entire comment. It assumes that '#' has been consumed.
// It will consume *up to* the first new line character, and pass control
// back to the last state on the stack.
func lexComment(lx *lexer) stateFn {
	r := lx.peek()
	if isNL(r) || r == eof {
		lx.emit(itemText)
		return lx.pop()
	}
	lx.next()
	return lexComment
}

// lexSkip ignores all slurped input and moves on to the next state.
func lexSkip(lx *lexer, nextState stateFn) stateFn {
	return func(lx *lexer) stateFn {
		lx.ignore()
		return nextState
	}
}

// Tests for both key separators
func isKeySeparator(r rune) bool {
	return r == keySepEqual || r == keySepColon
}

// isWhitespace returns true if `r` is a whitespace character according
// to the spec.
func isWhitespace(r rune) bool {
	return r == '\t' || r == ' '
}

func isNL(r rune) bool {
	return r == '\n' || r == '\r'
}

func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func isHexadecimal(r rune) bool {
	return (r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'f') ||
		(r >= 'A' && r <= 'F')
}

func (itype itemType) String() string {
	switch itype {
	case itemError:
		return "Error"
	case itemNIL:
		return "NIL"
	case itemEOF:
		return "EOF"
	case itemText:
		return "Text"
	case itemString:
		return "String"
	case itemBool:
		return "Bool"
	case itemInteger:
		return "Integer"
	case itemFloat:
		return "Float"
	case itemDatetime:
		return "DateTime"
	case itemKey:
		return "Key"
	case itemArrayStart:
		return "ArrayStart"
	case itemArrayEnd:
		return "ArrayEnd"
	case itemMapStart:
		return "MapStart"
	case itemMapEnd:
		return "MapEnd"
	case itemCommentStart:
		return "CommentStart"
	}
	panic(fmt.Sprintf("BUG: Unknown type '%s'.", itype))
}

func (item item) String() string {
	return fmt.Sprintf("(%s, '%s', %d)", item.typ.String(), item.val, item.line)
}

func escapeSpecial(c rune) string {
	switch c {
	case '\n':
		return "\\n"
	}
	return string(c)
}
