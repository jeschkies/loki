package logical

type LeafAccumulator struct {
	Leafs []Operator
}

func (v *LeafAccumulator) visitAggregation(a *Aggregation) {
	if a.Child() == nil {
		v.Leafs = append(v.Leafs, a)
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
	aggregations []*Aggregation
}

func (*AggregationAccumulator) visitBinary(*Binary) {}
func (*AggregationAccumulator) visitMap(*Map)       {}
func (*AggregationAccumulator) visitScan(*Scan)     {}
func (*AggregationAccumulator) visitFilter(*Filter) {}

func (acc *AggregationAccumulator) visitAggregation(a *Aggregation) {
	acc.aggregations = append(acc.aggregations, a)
}
