package logical

type LeafAccumulator struct {}

var _ Visitor[[]Operator] = &LeafAccumulator{}

func (v *LeafAccumulator) VisitAggregation(a *Aggregation) []Operator {
	if a.Child() != nil {
		return Dispatch[[]Operator](a.Child(), v)
	}
	return []Operator{a}

}

func (v *LeafAccumulator) VisitCoalescence(c *Coalescence) []Operator {
	if len(c.shards) == 0 {
		return []Operator{c}
	}

	var leafs []Operator
	for _, s := range c.shards {
		leafs = append(leafs, Dispatch[[]Operator](s, v)...)	
	}
	return leafs
}

func (v *LeafAccumulator) VisitBinary(b *Binary) []Operator {
	if b.lhs == nil && b.rhs == nil {
		return []Operator{b}
	}

	leafs := Dispatch[[]Operator](b.lhs, v)
	leafs = append(leafs, Dispatch[[]Operator](b.rhs, v)...)	
	return leafs
}

func (v *LeafAccumulator) VisitFilter(f *Filter) []Operator {
	if f.Child() != nil {
		return Dispatch[[]Operator](f.Child(), v)
	}
	return []Operator{f}
}

func (v *LeafAccumulator) VisitMap(m *Map) []Operator {
	if m.Child() != nil {
		return Dispatch[[]Operator](m.Child(), v)
	}
	return []Operator{m}
}

func (v *LeafAccumulator) VisitScan(s *Scan) []Operator {
	return []Operator{s}
}

type AggregationAccumulator struct {
	kind         string
}

var _ Visitor[[]*Aggregation] = &AggregationAccumulator{}

func (v *AggregationAccumulator) VisitAggregation(a *Aggregation) []*Aggregation {
	if a.Details.Name() != v.kind {
		return nil
	}
	acc := []*Aggregation{a}

	if a.Child() != nil {
		acc = append(acc, Dispatch[[]*Aggregation](a.Child(), v)...)
	}

	return acc
}

func (v *AggregationAccumulator)VisitCoalescence(c *Coalescence) []*Aggregation {
	var acc []*Aggregation
	for _, s := range c.shards {
		acc = append(acc, Dispatch[[]*Aggregation](s, v)...)
	}

	return acc
}

func (v *AggregationAccumulator)VisitBinary(*Binary) []*Aggregation {

}

func (v *AggregationAccumulator)VisitFilter(*Filter) T

func (v *AggregationAccumulator)VisitMap(*Map) T

func (v *AggregationAccumulator)VisitScan(*Scan) []*Aggregation {
	return nil
}

// ScanUpdate applies the update function to each scane visited.
type ScanUpdate struct {
	defaultVisitor
	apply func(*Scan)
}

func (update ScanUpdate) VisitScan(scan *Scan) {
	update.apply(scan)
}
