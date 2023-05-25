package logical

type LeafAccumulator struct{}

var _ Visitor[[]Operator] = &LeafAccumulator{}

func (v *LeafAccumulator) VisitAggregation(a *Aggregation) []Operator {
	if a.Child() != nil {
		return Dispatch[[]Operator](a.Child(), v)
	}
	return []Operator{a}

}

func (v *LeafAccumulator) VisitCoalescence(c *Coalescence) []Operator {
	if len(c.Shards) == 0 {
		return []Operator{c}
	}

	var leafs []Operator
	for _, s := range c.Shards {
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
	kind string
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

func (v *AggregationAccumulator) VisitCoalescence(c *Coalescence) []*Aggregation {
	var acc []*Aggregation
	for _, s := range c.Shards {
		acc = append(acc, Dispatch[[]*Aggregation](s, v)...)
	}

	return acc
}

func (v *AggregationAccumulator) VisitBinary(b *Binary) []*Aggregation {
	var acc []*Aggregation
	if b.lhs != nil {
		acc = append(acc, Dispatch[[]*Aggregation](b.lhs, v)...)
	}
	if b.rhs != nil {
		acc = append(acc, Dispatch[[]*Aggregation](b.rhs, v)...)
	}

	return acc
}

func (v *AggregationAccumulator) VisitFilter(f *Filter) []*Aggregation {
	if f.Child() != nil {
		return Dispatch[[]*Aggregation](f.Child(), v)
	}
	return nil
}

func (v *AggregationAccumulator) VisitMap(m *Map) []*Aggregation {
	if m.Child() != nil {
		return Dispatch[[]*Aggregation](m.Child(), v)
	}
	return nil
}

func (v *AggregationAccumulator) VisitScan(*Scan) []*Aggregation {
	return nil
}

// ScanUpdate applies the update function to each scane visited.
type ScanUpdate struct {
	updateVisitor
}

var _ Visitor[Unit] = &ScanUpdate{}

func NewScanUpdate(u func(*Scan)) *ScanUpdate {
	s := &ScanUpdate{}
	s.updateVisitor.updateScan = u
	return s
}
