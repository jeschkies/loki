package logical

import (
	"fmt"
	"io"
)

type Stringer struct {
	io.StringWriter
}

var _ Visitor = &Stringer{}

func NewStringer(w io.StringWriter) *Stringer {
	return &Stringer{StringWriter: w}
}

func (s *Stringer) visitAggregation(a *Aggregation) {
	s.WriteString(fmt.Sprintf("Aggregation(kind=%s", a.Details.Name()))
	if a.child != nil {
		s.WriteString(", ")
		// TODO: a.child.Accept(s)
	}
	s.WriteString(")")
}

func (s *Stringer) visitBinary(b *Binary) {
	s.WriteString(fmt.Sprintf("Binary(kind=%s, %s, %s)", b.Kind, b.lhs, b.rhs))
}

func (s *Stringer) visitCoalescence(*Coalescence) {
	// TODO show children
	s.WriteString(fmt.Sprintf("Coalescence(kind=??)"))
}

func (s *Stringer) visitFilter(f *Filter) {
	//return fmt.Sprintf("Filter(ty=%s, match='%s')", f.ty, f.match)
	if f.child != nil {
		s.WriteString(fmt.Sprintf("Filter(kind=%s, %s)", f.Kind, "???"))
	}
	s.WriteString(fmt.Sprintf("Filter(kind=%s)", f.Kind))
}

func (s *Stringer) visitMap(m *Map) {
	if m.child != nil {
		s.WriteString(fmt.Sprintf("Map(kind=%s, %s)", m.Kind, "???"))
	}
	s.WriteString(fmt.Sprintf("Map(kind=%s)", m.Kind))
}

func (s *Stringer) visitScan(scan *Scan) {
	s.WriteString(fmt.Sprintf("Scan(labels=%s)", scan.Labels()))
}
