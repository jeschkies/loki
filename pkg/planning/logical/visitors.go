package logical

type defaultVisitor struct{}

func (defaultVisitor) visitAggregation(*Aggregation) {}
func (defaultVisitor) visitBinary(*Binary)           {}
func (defaultVisitor) visitCoalescence(*Coalescence) {}
func (defaultVisitor) visitFilter(*Filter)           {}
func (defaultVisitor) visitMap(*Map)                 {}
func (defaultVisitor) visitScan(*Scan)               {}

type LeafAccumulator struct {
	Leafs []Operator
}

func (v *LeafAccumulator) visitAggregation(a *Aggregation) {
	if a.Child() == nil {
		v.Leafs = append(v.Leafs, a)
	}
}

func (v *LeafAccumulator) visitCoalescence(c *Coalescence) {
	if len(c.shards) == 0 {
		v.Leafs = append(v.Leafs, c)
	}
}

func (v *LeafAccumulator) visitBinary(b *Binary) {
	if b.lhs == nil && b.rhs == nil {
		v.Leafs = append(v.Leafs, b)
	}
}

func (v *LeafAccumulator) visitFilter(f *Filter) {
	if f.Child() == nil {
		v.Leafs = append(v.Leafs, f)
	}
}

func (v *LeafAccumulator) visitMap(m *Map) {
	if m.Child() == nil {
		v.Leafs = append(v.Leafs, m)
	}
}

func (v *LeafAccumulator) visitScan(s *Scan) {
	if s.Child() == nil {
		v.Leafs = append(v.Leafs, s)
	}
}

type AggregationAccumulator struct {
	defaultVisitor
	aggregations []*Aggregation
	kind         string
}

func (acc *AggregationAccumulator) visitAggregation(a *Aggregation) {
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

func (update ScanUpdate) visitScan(scan *Scan) {
	update.apply(scan)
}
