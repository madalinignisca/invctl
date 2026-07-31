package render

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

func logError(err error) { slog.Error("render failed", "error", err) }

// funcs are the template helpers.
//
// These are formatting and presentation only. Business logic stays in Go --
// a template that decides whether a service is healthy is a template nobody
// can test.
func funcs() template.FuncMap {
	return template.FuncMap{
		"dict":           dict,
		"hasField":       hasField,
		"since":          since,
		"shortTime":      shortTime,
		"statusClass":    statusClass,
		"statusLabel":    statusLabel,
		"healthClass":    healthClass,
		"lifecycleClass": lifecycleClass,
		"tierClass":      tierClass,
		"natureClass":    natureClass,
		"natureDesc":     domain.NatureDescription,
		"deref":          deref,
		"derefInt":       derefInt,
		"join":           strings.Join,
		"title":          titleCase,
		"pluralise":      pluralise,
		"queryString":    queryString,
		"add": func(nums ...int) int {
			total := 0
			for _, n := range nums {
				total += n
			}
			return total
		},
		"seq":    seq,
		"coord":  coord,
		"expiry": expiryOf,
	}
}

// coord formats an SVG coordinate.
//
// One decimal place, always: Go's default float formatting would put
// `1.0000000000000002` in an attribute, and a diagram whose numbers are
// rendered differently on two machines is not the same diagram. Rounding is
// safe because the layout's own metrics are whole pixels -- this is
// presentation, and the arithmetic that decided the number happened in Go.
func coord(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// seq returns 0..n-1 so a template can repeat an element a computed number of
// times -- Go templates cannot range over an integer.
func seq(n int) []int {
	if n <= 0 {
		return nil
	}
	// Guard against a pathological instance count rendering thousands of
	// elements into a table cell.
	if n > 64 {
		n = 64
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// dict builds a map inside a template, so a partial can be passed more than
// one value without inventing a struct for every call site.
func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, errors.New("dict: odd number of arguments")
	}
	m := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is not a string", i)
		}
		m[key] = values[i+1]
	}
	return m, nil
}

// hasField reports whether a dict carries a key, so shared partials can adapt
// without the caller supplying every optional value.
func hasField(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// since renders a stored RFC3339 timestamp as a relative age.
func since(stored string) string {
	t, err := domain.ParseTime(stored)
	if err != nil {
		return stored
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2 Jan 2006")
	}
}

// shortTime renders a stored timestamp for a table cell.
func shortTime(stored string) string {
	t, err := domain.ParseTime(stored)
	if err != nil {
		return stored
	}
	return t.Format("2006-01-02 15:04")
}

func statusClass(status domain.Status) string {
	switch status {
	case domain.StatusDown:
		return "pill pill-down"
	case domain.StatusDegraded:
		return "pill pill-degraded"
	default:
		return "pill pill-ok"
	}
}

func statusLabel(status domain.Status) string {
	switch status {
	case domain.StatusDown:
		return "Down"
	case domain.StatusDegraded:
		return "Degraded"
	default:
		return "OK"
	}
}

// healthClass colours an observed state.
//
// `unknown` is deliberately muted rather than alarming. It is what a stale
// reading renders as (docs/AUDIT.md rule 8), and "nobody is watching this any
// more" is a different problem from "this is down" -- it calls for looking at
// the collector, not at the box. Colouring them alike would send an operator to
// the wrong place at the worst time.
func healthClass(state domain.HealthState) string {
	switch state {
	case domain.HealthUp:
		return "pill pill-ok"
	case domain.HealthDegraded:
		return "pill pill-degraded"
	case domain.HealthDown:
		return "pill pill-down"
	default:
		return "pill pill-muted"
	}
}

func lifecycleClass(lifecycle string) string {
	switch lifecycle {
	case domain.LifecycleRetired:
		return "pill pill-muted"
	case domain.LifecycleMaintenance, domain.LifecycleDeprecated:
		return "pill pill-degraded"
	case domain.LifecyclePlanned:
		return "pill pill-info"
	default:
		return "pill pill-ok"
	}
}

func tierClass(tier int) string {
	if tier <= 1 {
		return "pill pill-tier1"
	}
	if tier == 2 {
		return "pill pill-tier2"
	}
	return "pill pill-muted"
}

// natureClass colours a dependency by how much damage it transmits, so the
// hard edges stand out in a long list.
func natureClass(nature string) string {
	switch nature {
	case domain.NatureHard:
		return "pill pill-down"
	case domain.NatureStartup:
		return "pill pill-startup"
	case domain.NatureSoft, domain.NatureAsync:
		return "pill pill-degraded"
	default:
		return "pill pill-muted"
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

func titleCase(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func pluralise(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// queryString builds a filter query string, skipping empty values.
func queryString(pairs ...string) template.URL {
	if len(pairs)%2 != 0 {
		return ""
	}
	var parts []string
	for i := 0; i < len(pairs); i += 2 {
		if pairs[i+1] == "" {
			continue
		}
		parts = append(parts, pairs[i]+"="+template.URLQueryEscaper(pairs[i+1]))
	}
	if len(parts) == 0 {
		return ""
	}
	return template.URL("?" + strings.Join(parts, "&"))
}

// Expiry is the presentation form of an EOL date: what to show and how to
// colour it.
type Expiry struct {
	State string
	Class string
	Label string
	Date  string
}

// expiryOf renders a stored EOL date for display, returning nil when there is
// none so a template can write {{with expiry .EOLDate}}.
//
// The classification is domain.ExpiryState -- this is a bridge, not a second
// opinion. It reads the wall clock the way `since` above does, which is the
// boundary this file draws: a helper may ask what time it is to phrase
// something, but the rule for what counts as expired lives in the domain and is
// tested there against a fixed clock.
func expiryOf(eolDate *string) *Expiry {
	if eolDate == nil || *eolDate == "" {
		return nil
	}
	now := time.Now().UTC()
	state := domain.ExpiryState(eolDate, now, domain.ExpirySoonDays*24*time.Hour)
	e := &Expiry{State: state, Date: *eolDate, Class: "pill pill-muted"}

	days, ok := domain.DaysUntil(eolDate, now)
	if !ok {
		// Stored but unreadable. Show the raw value rather than hiding it: a
		// row somebody has to fix is more useful visible than tidy.
		e.Label = *eolDate
		return e
	}
	switch state {
	case domain.ExpiryExpired:
		e.Class = "pill pill-down"
		e.Label = "expired " + humanDays(-days) + " ago"
	case domain.ExpirySoon:
		e.Class = "pill pill-degraded"
		e.Label = "in " + humanDays(days)
	default:
		e.Class = "pill pill-ok"
		e.Label = "in " + humanDays(days)
	}
	return e
}

// humanDays phrases a day count at the precision the number deserves. "in 803
// days" is a false precision nobody reads; "in 2 years" is what a person says.
func humanDays(days int) string {
	switch {
	case days <= 0:
		return "0 days"
	case days == 1:
		return "1 day"
	case days < 60:
		return fmt.Sprintf("%d days", days)
	case days < 730:
		return fmt.Sprintf("%d months", days/30)
	default:
		return fmt.Sprintf("%d years", days/365)
	}
}

// Money formatting.
//
// A deliberately locale-FREE format: symbol, comma thousands, period decimal,
// always two places. Rendering EUR as 1.234,56 for one reader and 1,234.56 for
// another means the same page shows two different-looking numbers depending on
// who opens it, and an operator reading over somebody's shoulder during an
// incident is the audience this whole application is written for. Unambiguous
// beats idiomatic here.
//
// Two decimal places is an assumption, and it holds for every currency this is
// plausibly deployed in (EUR, RON, USD, GBP, CHF). A zero-decimal currency like
// JPY would render 100 yen as ¥1.00, which is why the assumption is written down
// rather than left in the arithmetic.
var currencySymbols = map[string]string{
	"EUR": "€", "USD": "$", "GBP": "£", "CHF": "CHF ", "RON": "lei ",
}

func moneyFormatter(currency string) func(int64) string {
	symbol, ok := currencySymbols[strings.ToUpper(currency)]
	if !ok {
		// An unknown code renders as the code itself. Better a reader sees
		// "SEK 1,234.56" than a bare number whose unit they have to guess.
		symbol = strings.ToUpper(currency) + " "
	}
	return func(minor int64) string {
		sign := ""
		if minor < 0 {
			sign, minor = "-", -minor
		}
		whole, cents := minor/100, minor%100
		return fmt.Sprintf("%s%s%s.%02d", sign, symbol, groupThousands(whole), cents)
	}
}

// amountMinor renders a figure for an INPUT rather than for a reader.
//
// Deliberately not `money`. That formatter groups thousands with a comma and
// prefixes a symbol, and parseAmountMinor rejects a value carrying both a comma
// and a point as genuinely ambiguous -- so a form pre-filled with "€8,400.00"
// offers the operator a value the same application refuses to take back. The
// round trip has to close: what a field renders, its own parser must accept.
func amountMinor(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

// groupThousands inserts a comma every three digits, from the right.
func groupThousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	lead := len(digits) % 3
	if lead > 0 {
		b.WriteString(digits[:lead])
	}
	for i := lead; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}
