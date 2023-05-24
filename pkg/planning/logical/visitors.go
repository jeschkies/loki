package logical

type defaultVisitor struct{}

func (defaultVisitor) VisitAggregation(*Aggregation) {}
func (defaultVisitor) VisitBinary(*Binary)           {}
func (defaultVisitor) VisitCoalescence(*Coalescence) {}
func (defaultVisitor) VisitFilter(*Filter)           {}
func (defaultVisitor) VisitMap(*Map)                 {}
func (defaultVisitor) VisitScan(*Scan)               {}

type LeafAccumulator struct {}

var _ Visitor[[]Operator] = &LeafAccumulator{}

func (v *LeafAccumulator) VisitAggregation(a *Aggregation) []Operator {
	if a.Child() != nil {
		return dispatch[[]Operator](a.Child(), v)
	}
	return []Operator{a}

}

func (v *LeafAccumulator) VisitCoalescence(c *Coalescence) []Operator {
	if len(c.shards) == 0 {
		return []Operator{c}
	}

	var leafs []Operator
	for _, s := range c.shards {
		leafs = append(leafs, dispatch[[]Operator](s, v)...)	
	}
	return leafs
}

func (v *LeafAccumulator) VisitBinary(b *Binary) []Operator {
	if b.lhs == nil && b.rhs == nil {
		return []Operator{b}
	}

	leafs := dispatch[[]Operator](b.lhs, v)
	leafs = append(leafs, dispatch[[]Operator](b.rhs, v)...)	
	return leafs
}

func (v *LeafAccumulator) VisitFilter(f *Filter) []Operator {
	if f.Child() != nil {
		return dispatch[[]Operator](f.Child(), v)
	}
	return []Operator{f}
}

func (v *LeafAccumulator) VisitMap(m *Map) []Operator {
	if m.Child() != nil {
		return dispatch[[]Operator](m.Child(), v)
	}
	return []Operator{m}
}

func (v *LeafAccumulator) VisitScan(s *Scan) []Operator {
	return []Operator{s}
}

type AggregationAccumulator struct {
	defaultVisitor
	aggregations []*Aggregation
	kind         string
}

func (acc *AggregationAccumulator) VisitAggregation(a *Aggregation) {
	if a.Details.Name() != acc.kind {
		return
	}

	acc.aggregations = append(acc.aggregations, a)
}

// ScanUpdate applies the update function to each scane visited.
type ScanUpdate struct {
	defaultVisitor
	apply func(*Scan)
}

func (update ScanUpdate) VisitScan(scan *Scan) {
	update.apply(scan)
}
