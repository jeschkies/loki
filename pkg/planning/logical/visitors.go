package logical

type defaultVisitor struct{}

func (defaultVisitor) VisitAggregation(*Aggregation) {}
func (defaultVisitor) VisitBinary(*Binary)           {}
func (defaultVisitor) VisitCoalescence(*Coalescence) {}
func (defaultVisitor) VisitFilter(*Filter)           {}
func (defaultVisitor) VisitMap(*Map)                 {}
func (defaultVisitor) VisitScan(*Scan)               {}

type LeafAccumulator struct {
	Leafs []Operator
}

func (v *LeafAccumulator) VisitAggregation(a *Aggregation) {
	if a.Child() == nil {
		v.Leafs = append(v.Leafs, a)
	}
}

func (v *LeafAccumulator) VisitCoalescence(c *Coalescence) {
	if len(c.shards) == 0 {
		v.Leafs = append(v.Leafs, c)
	}
}

func (v *LeafAccumulator) VisitBinary(b *Binary) {
	if b.lhs == nil && b.rhs == nil {
		v.Leafs = append(v.Leafs, b)
	}
}

func (v *LeafAccumulator) VisitFilter(f *Filter) {
	if f.Child() == nil {
		v.Leafs = append(v.Leafs, f)
	}
}

func (v *LeafAccumulator) VisitMap(m *Map) {
	if m.Child() == nil {
		v.Leafs = append(v.Leafs, m)
	}
}

func (v *LeafAccumulator) VisitScan(s *Scan) {
	if s.Child() == nil {
		v.Leafs = append(v.Leafs, s)
	}
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
