package logical

import (
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/regexp/syntax"
)

// RegexOptimizer simplifies or even replaces regular expression filters.
type RegexOptimizer struct{}

func (*RegexOptimizer) visitAggregation(*Aggregation) {}
func (*RegexOptimizer) visitBinary(*Binary)           {}
func (*RegexOptimizer) visitMap(*Map)                 {}
func (*RegexOptimizer) visitScan(*Scan)               {}

func (r *RegexOptimizer) visitFilter(f *Filter) {
	if f.ty != labels.MatchRegexp && f.ty != labels.MatchNotRegexp {
		return
	}

	reg, err := syntax.Parse(f.match, syntax.Perl)
	if err != nil {
		return
	}
	reg = reg.Simplify()

	switch reg.Op {
	case syntax.OpLiteral:
		f.match = string(reg.Rune)
		f.ty = labels.MatchEqual
	default:
		return
	}

}

/*
// parseRegexpFilter parses a regexp and attempt to simplify it with only literal filters.
// If not possible it will returns the original regexp filter.
func parseRegexpFilter(re string, match bool, isLabel bool) (Filterer, error) {
	reg, err := syntax.Parse(re, syntax.Perl)
	if err != nil {
		return nil, err
	}
	reg = reg.Simplify()

	// attempt to improve regex with tricks
	f, ok := simplify(reg, isLabel)
	if !ok {
		util.AllNonGreedy(reg)
		regex := reg.String()
		if isLabel {
			// label regexes are anchored to
			// the beginning and ending of lines
			regex = "^(?:" + regex + ")$"
		}
		return newRegexpFilter(regex, match)
	}
	if match {
		return f, nil
	}
	return newNotFilter(f), nil
}

// simplify a regexp expression by replacing it, when possible, with a succession of literal filters.
// For example `(foo|bar)` will be replaced by  `containsFilter(foo) or containsFilter(bar)`
func simplify(reg *syntax.Regexp, isLabel bool) (Filterer, bool) {
	switch reg.Op {
	case syntax.OpAlternate:
		return simplifyAlternate(reg, isLabel)
	case syntax.OpConcat:
		return simplifyConcat(reg, nil)
	case syntax.OpCapture:
		util.ClearCapture(reg)
		return simplify(reg, isLabel)
	case syntax.OpLiteral:
		if isLabel {
			return newEqualFilter([]byte(string(reg.Rune)), util.IsCaseInsensitive(reg)), true
		}
		return newContainsFilter([]byte(string(reg.Rune)), util.IsCaseInsensitive(reg)), true
	case syntax.OpStar:
		if reg.Sub[0].Op == syntax.OpAnyCharNotNL {
			return TrueFilter, true
		}
	case syntax.OpPlus:
		if len(reg.Sub) == 1 && reg.Sub[0].Op == syntax.OpAnyCharNotNL { // simplify ".+"
			return ExistsFilter, true
		}
	case syntax.OpEmptyMatch:
		return TrueFilter, true
	}
	return nil, false
}
*/

func ShardAggregations(p *Plan) *Plan {
	// TODO: we might want to chains visitors insteadsee
	// see https://www.lihaoyi.com/post/ZeroOverheadTreeProcessingwiththeVisitorPattern.html
	v := &AggregationAccumulator{}
	p.Root.Accept(v)  

	for _, a := range v.aggregations {
		// TODO: shard a
		// sharded := &ShardedAggregation{}
		p.Replace(a, a)
	}
}
