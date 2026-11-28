package plugin

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const (
	minMentionYear = 1900
	maxMentionYear = 2100
)

// monthNames maps folded English and French month names (and common
// abbreviations) to calendar month numbers 1..12.
var monthNames = map[string]int{
	"january": 1, "jan": 1, "janvier": 1, "janv": 1,
	"february": 2, "feb": 2, "fevrier": 2, "fevr": 2,
	"march": 3, "mar": 3, "mars": 3,
	"april": 4, "apr": 4, "avril": 4, "avr": 4,
	"may": 5, "mai": 5,
	"june": 6, "jun": 6, "juin": 6,
	"july": 7, "jul": 7, "juillet": 7, "juil": 7,
	"august": 8, "aug": 8, "aout": 8,
	"september": 9, "sep": 9, "sept": 9, "septembre": 9,
	"october": 10, "oct": 10, "octobre": 10,
	"november": 11, "nov": 11, "novembre": 11,
	"december": 12, "dec": 12, "decembre": 12,
}

type queryDate struct {
	Year  int
	Month int // 0 when year-only, 1..12 when month is specified
}

// metadataTokenSet builds a folded, lightly-stemmed token set from semantic
// CommonMetadata fields (subjects, tags, place, country, author, source).
func metadataTokenSet(m *quivrplugin.CommonMetadata) map[string]bool {
	if m == nil {
		return nil
	}
	var set map[string]bool
	add := func(s string) {
		if strings.TrimSpace(s) == "" {
			return
		}
		for _, tok := range tokenizeContent(s) {
			if set == nil {
				set = map[string]bool{}
			}
			set[tok] = true
		}
	}
	for _, v := range m.Subjects {
		add(v)
	}
	for _, v := range m.Tags {
		add(v)
	}
	for _, v := range m.Place {
		add(v)
	}
	for _, v := range m.Country {
		add(v)
	}
	for _, v := range m.Author {
		add(v)
	}
	add(m.Source)
	return set
}

// parsePublishedAt parses CommonMetadata.PublishedAt (RFC3339 / RFC3339Nano),
// normalizing lowercase 't'/'z' separators permitted by common-metadata.schema.json.
func parsePublishedAt(m *quivrplugin.CommonMetadata) (time.Time, bool) {
	if m == nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(m.PublishedAt)
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.ToUpper(raw))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func parseDayOfMonth(tok string) bool {
	if len(tok) < 1 || len(tok) > 2 || !isAllDigits(tok) {
		return false
	}
	d, err := strconv.Atoi(tok)
	return err == nil && d >= 1 && d <= 31
}

func parseMentionYear(tok string) (int, bool) {
	if len(tok) != 4 || !isAllDigits(tok) {
		return 0, false
	}
	y, err := strconv.Atoi(tok)
	if err != nil || y < minMentionYear || y > maxMentionYear {
		return 0, false
	}
	return y, true
}

// extractQueryDates extracts explicit calendar year and month+year mentions
// from a query (ISO YYYY-MM / YYYY-MM-DD, EN/FR month+year, and standalone
// 4-digit years). Strictly deterministic.
func extractQueryDates(query string) []queryDate {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil
	}

	seen := map[queryDate]bool{}
	var out []queryDate
	add := func(qd queryDate) {
		if !seen[qd] {
			seen[qd] = true
			out = append(out, qd)
		}
	}

	// First pass: detect ISO YYYY-MM or YYYY-MM-DD tokens before splitting on '-'.
	isoYears := map[int]int{}
	for _, field := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	}) {
		parts := strings.Split(field, "-")
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		y, ok := parseMentionYear(parts[0])
		if !ok || len(parts[1]) != 2 || !isAllDigits(parts[1]) {
			continue
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil || m < 1 || m > 12 {
			continue
		}
		if len(parts) == 3 {
			if len(parts[2]) != 2 || !isAllDigits(parts[2]) {
				continue
			}
			d, derr := strconv.Atoi(parts[2])
			if derr != nil || d < 1 || d > 31 {
				continue
			}
		}
		add(queryDate{Year: y, Month: m})
		isoYears[y]++
	}

	// Second pass: natural-language tokens (month + year, year + month, standalone year).
	raw := strings.FieldsFunc(trimmed, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := make([]string, 0, len(raw))
	for _, r := range raw {
		if f := foldWord(r); f != "" {
			tokens = append(tokens, f)
		}
	}

	pairedYearIdx := make([]bool, len(tokens))
	for i, tok := range tokens {
		month, isMonth := monthNames[tok]
		if !isMonth {
			continue
		}
		if i+1 < len(tokens) {
			if y, ok := parseMentionYear(tokens[i+1]); ok {
				add(queryDate{Year: y, Month: month})
				pairedYearIdx[i+1] = true
				continue
			}
		}
		if i+2 < len(tokens) && parseDayOfMonth(tokens[i+1]) {
			if y, ok := parseMentionYear(tokens[i+2]); ok {
				add(queryDate{Year: y, Month: month})
				pairedYearIdx[i+2] = true
				continue
			}
		}
		if i > 0 {
			if y, ok := parseMentionYear(tokens[i-1]); ok {
				add(queryDate{Year: y, Month: month})
				pairedYearIdx[i-1] = true
			}
		}
	}

	for i, tok := range tokens {
		if pairedYearIdx[i] {
			continue
		}
		y, ok := parseMentionYear(tok)
		if !ok {
			continue
		}
		if isoYears[y] > 0 {
			isoYears[y]--
			continue
		}
		add(queryDate{Year: y})
	}
	return out
}

// scoreDateAlignment scores how closely a publication timestamp matches any
// explicit date mention in the query, returning a value in [0, 1].
func scoreDateAlignment(pub time.Time, dates []queryDate) float64 {
	best := 0.0
	pubYear := pub.Year()
	pubMonth := int(pub.Month())
	for _, qd := range dates {
		var s float64
		if qd.Month > 0 {
			monthDiff := (pubYear-qd.Year)*12 + (pubMonth - qd.Month)
			if monthDiff < 0 {
				monthDiff = -monthDiff
			}
			switch {
			case monthDiff == 0:
				s = 1.0
			case monthDiff == 1:
				s = 0.85
			case pubYear == qd.Year:
				s = 0.65
			case monthDiff <= 3:
				s = 0.40
			case pubYear-qd.Year == 1 || qd.Year-pubYear == 1:
				s = 0.20
			}
		} else {
			yearDiff := pubYear - qd.Year
			if yearDiff < 0 {
				yearDiff = -yearDiff
			}
			switch yearDiff {
			case 0:
				s = 1.0
			case 1:
				s = 0.25
			}
		}
		if s > best {
			best = s
		}
	}
	return best
}

// scorePoolRecency computes deterministic pool-relative exponential freshness
// decay against the newest published_at timestamp present in the pool.
// Returns all zeros when fewer than 2 distinct timestamps exist.
func scorePoolRecency(pubTimes []time.Time, hasPub []bool, halfLifeDays float64) []float64 {
	out := make([]float64, len(pubTimes))
	if halfLifeDays <= 0 {
		halfLifeDays = 30
	}
	var minPub, maxPub time.Time
	dated := 0
	for i, ok := range hasPub {
		if !ok {
			continue
		}
		t := pubTimes[i]
		if dated == 0 {
			minPub, maxPub = t, t
		} else {
			if t.Before(minPub) {
				minPub = t
			}
			if t.After(maxPub) {
				maxPub = t
			}
		}
		dated++
	}
	if dated < 2 || !maxPub.After(minPub) {
		return out
	}
	for i, ok := range hasPub {
		if !ok {
			continue
		}
		ageDays := max(0.0, maxPub.Sub(pubTimes[i]).Hours()/24.0)
		out[i] = math.Pow(0.5, ageDays/halfLifeDays)
	}
	return out
}
