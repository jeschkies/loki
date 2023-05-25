package logical

import (
	"github.com/prometheus/prometheus/model/labels"

	regexp "github.com/grafana/regexp/syntax"

	"github.com/grafana/loki/pkg/logql/syntax"
)

type unit struct{}

type updateVisitor struct {
	updateFilter func(*Filter)
	updateScan   func(*Scan)
}

var _ Visitor[unit] = updateVisitor{}

func (v updateVisitor) VisitAggregation(a *Aggregation) unit {
	if a.Child() != nil {
		Dispatch[unit](a.Child(), v)
	}
	return unit{}
}

func (v updateVisitor) VisitCoalescence(c *Coalescence) unit {
	for _, s := range c.shards {
		Dispatch[unit](s, v)
	}
	return unit{}
}

func (v updateVisitor) VisitBinary(b *Binary) unit {
	if b.lhs != nil {
		Dispatch[unit](b.lhs, v)
	}
	if b.rhs != nil {
		Dispatch[unit](b.rhs, v)
	}
	return unit{}
}

func (v updateVisitor) VisitFilter(f *Filter) unit {
	if v.updateFilter != nil {
		v.updateFilter(f)
	}
	if f.Child() != nil {
		Dispatch[unit](f.Child(), v)
	}
	return unit{}
}

func (v updateVisitor) VisitMap(m *Map) unit {
	if m.Child() != nil {
		Dispatch[unit](m.Child(), v)
	}
	return unit{}
}

func (v updateVisitor) VisitScan(s *Scan) unit {
	if v.updateScan != nil {
		v.updateScan(s)
	}
	return unit{}
}

// RegexOptimizer simplifies or even replaces regular expression filters.
type RegexOptimizer struct {
	updateVisitor
}

var _ Visitor[unit] = &RegexOptimizer{}

func NewRegexpOptimizer() *RegexOptimizer {
	r := &RegexOptimizer{}
	r.updateVisitor.updateFilter = r.optimize

	return r
}

func (r *RegexOptimizer) optimize(f *Filter) {
	if f.ty != labels.MatchRegexp && f.ty != labels.MatchNotRegexp {
		return
	}

	reg, err := regexp.Parse(f.match, regexp.Perl)
	if err != nil {
		return
	}
	reg = reg.Simplify()

	switch reg.Op {
	case regexp.OpLiteral:
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

type ShardResolver interface {
	Shards(matchers *syntax.MatcherRange) (int, uint64, error)
}

type ConstantShards int

func (s ConstantShards) Shards(_ *syntax.MatcherRange) (int, uint64, error) { return int(s), 0, nil }

// TODO: use visitor instead.
func ShardAggregations(p *Plan, resolver ShardResolver) *Plan {
	// TODO: we might want to chains visitors instead
	// see https://www.lihaoyi.com/post/ZeroOverheadTreeProcessingwiththeVisitorPattern.html
	v := &AggregationAccumulator{kind: "sum"}
	aggregations := Dispatch[[]*Aggregation](p.Root, v)

	c := NewCoalescene()
	shards, _, _ := resolver.Shards(nil)
	// TODO: check for nested aggregations
	for _, a := range aggregations {
		for i := shards - 1; i >= 0; i-- {
			updated := a.DeepClone()
			Dispatch[unit](updated, NewScanUpdate(func(s *Scan) {
				s.shard = &ShardAnnotation{i, shards}
			}))
			c.shards = append(c.shards, updated)
		}
		p.Replace(a, c)
	}
	return p
}
