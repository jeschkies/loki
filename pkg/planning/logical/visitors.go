package logical

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
}

func (acc *AggregationAccumulator) visitAggregation(a *Aggregation) {
	acc.aggregations = append(acc.aggregations, a)
}
