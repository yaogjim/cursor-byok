package query

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Predicate struct {
	where string
	args  []any
}

func (predicate Predicate) WhereSQL() string {
	if strings.TrimSpace(predicate.where) == "" {
		return "1 = 1"
	}
	return predicate.where
}

func (predicate Predicate) Args() []any {
	return append([]any(nil), predicate.args...)
}

type expression interface {
	compile() (Predicate, error)
}

type termExpression struct {
	field string
	value string
}

type notExpression struct{ child expression }
type andExpression struct{ children []expression }
type orExpression struct{ children []expression }

type tokenKind int

const (
	tokenWord tokenKind = iota
	tokenLeftParen
	tokenRightParen
	tokenMinus
)

type token struct {
	kind  tokenKind
	value string
}

type parser struct {
	tokens []token
	index  int
}

var textFields = map[string]string{
	"project":        "e.project_id",
	"project_id":     "e.project_id",
	"session":        "e.app_session_id",
	"conversation":   "e.conversation_id",
	"turn":           "e.turn_id",
	"trace":          "e.trace_id",
	"request":        "e.cursor_request_id",
	"http_request":   "e.http_request_id",
	"model_call":     "e.model_call_id",
	"tool_call":      "e.tool_call_id",
	"severity":       "e.severity",
	"capability":     "e.capability",
	"operation":      "e.operation",
	"direction":      "e.direction",
	"layer":          "e.layer",
	"event":          "e.event",
	"route":          "e.route",
	"target":         "e.execution_target",
	"protocol":       "e.protocol",
	"implementation": "e.implementation_state",
	"outcome":        "e.semantic_outcome",
	"error":          "e.error_category",
	"status":         "e.status",
	"source":         "m.source_kind",
}

func Compile(input string) (Predicate, error) {
	tokens, err := lex(input)
	if err != nil {
		return Predicate{}, err
	}
	if len(tokens) == 0 {
		return Predicate{where: "1 = 1"}, nil
	}
	parsed := parser{tokens: tokens}
	expression, err := parsed.parseOr()
	if err != nil {
		return Predicate{}, err
	}
	if parsed.index != len(tokens) {
		return Predicate{}, fmt.Errorf("unexpected token %q", tokens[parsed.index].value)
	}
	return expression.compile()
}

func lex(input string) ([]token, error) {
	runes := []rune(strings.TrimSpace(input))
	result := make([]token, 0, len(runes)/4)
	for index := 0; index < len(runes); {
		if unicode.IsSpace(runes[index]) {
			index++
			continue
		}
		switch runes[index] {
		case '(':
			result = append(result, token{kind: tokenLeftParen, value: "("})
			index++
			continue
		case ')':
			result = append(result, token{kind: tokenRightParen, value: ")"})
			index++
			continue
		case '-':
			result = append(result, token{kind: tokenMinus, value: "-"})
			index++
			continue
		}
		var builder strings.Builder
		quoted := false
		for index < len(runes) {
			character := runes[index]
			if character == '"' {
				quoted = !quoted
				index++
				continue
			}
			if !quoted && (unicode.IsSpace(character) || character == '(' || character == ')') {
				break
			}
			builder.WriteRune(character)
			index++
		}
		if quoted {
			return nil, errors.New("unterminated quoted phrase")
		}
		value := strings.TrimSpace(builder.String())
		if value == "" {
			return nil, errors.New("empty query token")
		}
		result = append(result, token{kind: tokenWord, value: value})
	}
	return result, nil
}

func (parser *parser) parseOr() (expression, error) {
	left, err := parser.parseAnd()
	if err != nil {
		return nil, err
	}
	children := []expression{left}
	for parser.matchWord("OR") {
		right, err := parser.parseAnd()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return left, nil
	}
	return orExpression{children: children}, nil
}

func (parser *parser) parseAnd() (expression, error) {
	children := make([]expression, 0, 4)
	for parser.index < len(parser.tokens) {
		current := parser.tokens[parser.index]
		if current.kind == tokenRightParen || (current.kind == tokenWord && strings.EqualFold(current.value, "OR")) {
			break
		}
		child, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	if len(children) == 0 {
		return nil, errors.New("query expression is incomplete")
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return andExpression{children: children}, nil
}

func (parser *parser) parseUnary() (expression, error) {
	if parser.index < len(parser.tokens) && parser.tokens[parser.index].kind == tokenMinus {
		parser.index++
		child, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		return notExpression{child: child}, nil
	}
	if parser.matchWord("NOT") {
		child, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		return notExpression{child: child}, nil
	}
	return parser.parsePrimary()
}

func (parser *parser) parsePrimary() (expression, error) {
	if parser.index >= len(parser.tokens) {
		return nil, errors.New("query expression is incomplete")
	}
	current := parser.tokens[parser.index]
	if current.kind == tokenLeftParen {
		parser.index++
		child, err := parser.parseOr()
		if err != nil {
			return nil, err
		}
		if parser.index >= len(parser.tokens) || parser.tokens[parser.index].kind != tokenRightParen {
			return nil, errors.New("missing closing parenthesis")
		}
		parser.index++
		return child, nil
	}
	if current.kind != tokenWord {
		return nil, fmt.Errorf("unexpected token %q", current.value)
	}
	parser.index++
	field := "keyword"
	value := current.value
	if separator := strings.IndexRune(current.value, ':'); separator >= 0 {
		field = strings.ToLower(strings.TrimSpace(current.value[:separator]))
		value = strings.TrimSpace(current.value[separator+1:])
	}
	if field == "" || value == "" {
		return nil, fmt.Errorf("invalid query term %q", current.value)
	}
	return termExpression{field: field, value: value}, nil
}

func (parser *parser) matchWord(value string) bool {
	if parser.index >= len(parser.tokens) {
		return false
	}
	current := parser.tokens[parser.index]
	if current.kind != tokenWord || !strings.EqualFold(current.value, value) {
		return false
	}
	parser.index++
	return true
}

func (expression termExpression) compile() (Predicate, error) {
	if column, ok := textFields[expression.field]; ok {
		return compileText(column, expression.value), nil
	}
	switch expression.field {
	case "keyword":
		return compileKeyword(expression.value), nil
	case "time":
		return compileTimeRange(expression.value)
	case "duration", "duration_ms":
		return compileNumber("e.duration_ms", expression.value)
	case "status_code":
		return compileNumber("CAST(json_extract(e.safe_fields_json, '$.status_code') AS INTEGER)", expression.value)
	case "dropped", "dropped_events":
		return compileNumber("CAST(e.dropped_events_key AS INTEGER)", expression.value)
	case "has_payload":
		return compileBooleanPresence("e.payload_ref", expression.value)
	case "decode_error":
		return compileBooleanInteger("e.decode_error", expression.value)
	default:
		return Predicate{}, fmt.Errorf("unsupported query field %q", expression.field)
	}
}

func (expression notExpression) compile() (Predicate, error) {
	child, err := expression.child.compile()
	if err != nil {
		return Predicate{}, err
	}
	return Predicate{where: "NOT (" + child.WhereSQL() + ")", args: child.Args()}, nil
}

func (expression andExpression) compile() (Predicate, error) {
	return compileGroup("AND", expression.children)
}

func (expression orExpression) compile() (Predicate, error) {
	return compileGroup("OR", expression.children)
}

func compileGroup(operator string, children []expression) (Predicate, error) {
	clauses := make([]string, 0, len(children))
	args := make([]any, 0, len(children))
	for _, child := range children {
		compiled, err := child.compile()
		if err != nil {
			return Predicate{}, err
		}
		clauses = append(clauses, "("+compiled.WhereSQL()+")")
		args = append(args, compiled.Args()...)
	}
	return Predicate{where: strings.Join(clauses, " "+operator+" "), args: args}, nil
}

func compileText(column string, value string) Predicate {
	return Predicate{where: "COALESCE(" + column + ", '') = ?", args: []any{strings.TrimSpace(value)}}
}

func compileKeyword(value string) Predicate {
	pattern := "%" + escapeLike(strings.TrimSpace(value)) + "%"
	columns := []string{
		"e.layer", "e.event", "e.route", "e.execution_target", "e.error_category", "e.safe_fields_json",
	}
	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		clauses = append(clauses, "COALESCE("+column+", '') LIKE ? ESCAPE '\\'")
		args = append(args, pattern)
	}
	return Predicate{where: "(" + strings.Join(clauses, " OR ") + ")", args: args}
}

func compileTimeRange(value string) (Predicate, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "..") {
		return compileRecentTime(value, time.Now().UTC())
	}
	parts := strings.SplitN(value, "..", 2)
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if strings.TrimSpace(parts[0]) != "" {
		start, err := parseTimeBoundary(strings.TrimSpace(parts[0]), false)
		if err != nil {
			return Predicate{}, fmt.Errorf("invalid time range start: %w", err)
		}
		start = start.UTC()
		clauses = append(clauses, "(e.timestamp_seconds > ? OR (e.timestamp_seconds = ? AND e.timestamp_nanoseconds >= ?))")
		args = append(args, start.Unix(), start.Unix(), start.Nanosecond())
	}
	if strings.TrimSpace(parts[1]) != "" {
		end, err := parseTimeBoundary(strings.TrimSpace(parts[1]), true)
		if err != nil {
			return Predicate{}, fmt.Errorf("invalid time range end: %w", err)
		}
		end = end.UTC()
		clauses = append(clauses, "(e.timestamp_seconds < ? OR (e.timestamp_seconds = ? AND e.timestamp_nanoseconds <= ?))")
		args = append(args, end.Unix(), end.Unix(), end.Nanosecond())
	}
	if len(clauses) == 0 {
		return Predicate{}, errors.New("time range requires at least one boundary")
	}
	return Predicate{where: strings.Join(clauses, " AND "), args: args}, nil
}

func compileRecentTime(value string, now time.Time) (Predicate, error) {
	duration, err := parseRecentDuration(value)
	if err != nil {
		return Predicate{}, err
	}
	start := now.Add(-duration).UTC()
	return Predicate{
		where: "(e.timestamp_seconds > ? OR (e.timestamp_seconds = ? AND e.timestamp_nanoseconds >= ?))",
		args:  []any{start.Unix(), start.Unix(), start.Nanosecond()},
	}, nil
}

func parseRecentDuration(value string) (time.Duration, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 32)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid recent time duration %q", value)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid recent time duration %q", value)
	}
	return duration, nil
}

func parseTimeBoundary(value string, endOfDay bool) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	if endOfDay {
		return parsed.Add(24*time.Hour - time.Nanosecond).UTC(), nil
	}
	return parsed.UTC(), nil
}

func compileNumber(column string, value string) (Predicate, error) {
	operator, numberText := splitComparison(value)
	number, err := strconv.ParseInt(numberText, 10, 64)
	if err != nil {
		return Predicate{}, fmt.Errorf("invalid numeric value %q", value)
	}
	return Predicate{where: column + " " + operator + " ?", args: []any{number}}, nil
}

func splitComparison(value string) (string, string) {
	value = strings.TrimSpace(value)
	for _, operator := range []string{">=", "<=", "!=", ">", "<", "="} {
		if strings.HasPrefix(value, operator) {
			return operator, strings.TrimSpace(strings.TrimPrefix(value, operator))
		}
	}
	return "=", value
}

func compileBooleanPresence(column string, value string) (Predicate, error) {
	boolean, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return Predicate{}, fmt.Errorf("invalid boolean value %q", value)
	}
	if boolean {
		return Predicate{where: "COALESCE(" + column + ", '') <> ''"}, nil
	}
	return Predicate{where: "COALESCE(" + column + ", '') = ''"}, nil
}

func compileBooleanInteger(column string, value string) (Predicate, error) {
	boolean, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return Predicate{}, fmt.Errorf("invalid boolean value %q", value)
	}
	if boolean {
		return Predicate{where: column + " = 1"}, nil
	}
	return Predicate{where: column + " = 0"}, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
