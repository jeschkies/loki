package logical

import (
	"fmt"
	"strings"
)

type Stringer struct {}

var _ Visitor[string] = &Stringer{}

func NewStringer() *Stringer {
	return &Stringer{}
}

func (s *Stringer) VisitAggregation(a *Aggregation) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Aggregation(kind=%s", a.Details.Name()))
	if a.child != nil {
		sb.WriteString(", ")
		sb.WriteString(dispatch[string](a.child, s))
	}
	sb.WriteString(")")
	return sb.String()
}

func (s *Stringer) VisitBinary(b *Binary) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Binary(kind=%s, %s, %s)", b.Kind, dispatch(b.lhs, s), dispatch(b.rhs, s)))
	return sb.String()
}

func (s *Stringer) VisitCoalescence(*Coalescence) string {
	// TODO show children
	return fmt.Sprintf("Coalescence(kind=??)")
}

func (s *Stringer) VisitFilter(f *Filter) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Filter(kind=%s", f.Kind))
	if f.child != nil {
		sb.WriteString(fmt.Sprintf(", %s", dispatch(f.child, s)))
	}
	sb.WriteString(")")
	return sb.String()
}

func (s *Stringer) VisitMap(m *Map) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Map(kind=%s", m.Kind))
	if m.child != nil {
		sb.WriteString(fmt.Sprintf(", %s", dispatch(m.child, s)))
	}
	sb.WriteString(")")
	return sb.String()
}

func (s *Stringer) VisitScan(scan *Scan) string {
	return fmt.Sprintf("Scan(labels=%s)", scan.Labels())
}
