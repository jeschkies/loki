package logicalplan

import (
	"fmt"
	"io"
	"strings"

	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/pkg/logql/syntax"
)

type Plan struct {
	Root Operator
}

func (p *Plan) String() string {
	return p.Root.String()
}

func (p *Plan) Graphviz(w io.StringWriter) {
	w.WriteString(`digraph { node [shape=rect];rankdir="BT";`)

	visitor := &Graphviz{id: 0, writer: w}
	p.Root.Accept(visitor)

	w.WriteString("}")
}

type Graphviz struct {
	id     int
	writer io.StringWriter
}

func (g *Graphviz) visitAggregation(a *Aggregation) {
	g.writer.WriteString(fmt.Sprintf(`%d [label="Aggregation:%s"];`, g.id, a.Details.Name()))
	if a.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`%d -> %d;`, g.id+1, g.id))
	}
	g.id++
}

func (g *Graphviz) visitFilter(f *Filter) {
	g.writer.WriteString(fmt.Sprintf(`%d [label="Filter"];`, g.id))
	if f.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`%d -> %d;`, g.id+1, g.id))
	}
	g.id++
}

func (g *Graphviz) visitMap(m *Map) {
	g.writer.WriteString(fmt.Sprintf(`%d [label="Map"];`, g.id))
	if m.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`%d -> %d;`, g.id+1, g.id))
	}
	g.id++
}

func (g *Graphviz) visitScan(s *Scan) {
	g.writer.WriteString(fmt.Sprintf(`%d [label="Scan"];`, g.id))
	if s.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`%d -> %d;`, g.id+1, g.id))
	}
	g.id++
}

type Operator interface {
	Child() Operator
	SetChild(Operator)
	String() string
	Accept(Visitor)
}

type Visitor interface {
	visitAggregation(*Aggregation)
	visitFilter(*Filter)
	visitMap(*Map)
	visitScan(*Scan)
}

type Parent struct {
	child Operator
}

func (p *Parent) Child() Operator {
	return p.child
}

func (p *Parent) SetChild(o Operator) {
	p.child = o
}

type AggregationDetails interface {
	Name() string
}

type Aggregation struct {
	Details AggregationDetails
	Parent
}

func (a *Aggregation) String() string {
	if a.child != nil {
		return fmt.Sprintf("Aggregation(kind=%s, %s)", a.Details.Name(), a.child.String())
	}
	return fmt.Sprintf("Aggregation(kind=%s)", a.Details.Name())
}

func (a *Aggregation) Accept(v Visitor) {
	v.visitAggregation(a)
	if a.Child() != nil {
		a.Child().Accept(v)
	}
}

type Sum struct{}

func (s *Sum) Name() string {
	return "sum"
}

type Rate struct{}

func (s *Rate) Name() string {
	return "rate"
}

type Filter struct {
	op    string
	match string
	ty    labels.MatchType
	Kind  string
	Parent
}

func (f *Filter) String() string {
	//return fmt.Sprintf("Filter(ty=%s, match='%s')", f.ty, f.match)
	if f.child != nil {
		return fmt.Sprintf("Filter(kind=%s, %s)", f.Kind, f.child.String())
	}
	return fmt.Sprintf("Filter(kind=%s)", f.Kind)
}

func (f *Filter) Accept(v Visitor) {
	v.visitFilter(f)
	if f.Child() != nil {
		f.Child().Accept(v)
	}
}

type Map struct {
	Kind string
	Parent
}

func (m *Map) String() string {
	if m.child != nil {
		return fmt.Sprintf("Map(kind=%s, %s)", m.Kind, m.child)
	}
	return fmt.Sprintf("Map(kind=%s)", m.Kind)
}

func (m *Map) Accept(v Visitor) {
	v.visitMap(m)
	if m.Child() != nil {
		m.Child().Accept(v)
	}
}

type Binary struct {
	Kind string
	lhs  Operator
	rhs  Operator
}

func (b *Binary) String() string {
	return fmt.Sprintf("Binary(kind=%s, %s, %s)", b.Kind, b.lhs, b.rhs)
}

func (b *Binary) Accept(v Visitor) {
	// TODO
}

func (b *Binary) Child() Operator {
	return nil // TODO
}

func (b *Binary) SetChild(o Operator) {
	// TODO
}

type Scan struct {
	Labels string
}

func (s *Scan) Child() Operator {
	return nil
}

func (s *Scan) SetChild(_ Operator) {}

func (s *Scan) String() string {
	return fmt.Sprintf("Scan(labels=%s)", s.Labels)
}

func (s *Scan) Accept(v Visitor) {
	v.visitScan(s)
	if s.Child() != nil {
		s.Child().Accept(v)
	}
}

func Build(query string) (*Plan, error) {
	expr, err := syntax.ParseExpr(query)
	if err != nil {
		return nil, err
	}

	r, err := build(expr)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Root: r}
	return plan, nil
}

func build(expr syntax.Expr) (Operator, error) {
	switch concrete := expr.(type) {
	case *syntax.VectorAggregationExpr:
		var details AggregationDetails
		if concrete.Operation == syntax.OpTypeSum {
			details = &Sum{}
		}
		p, err := build(concrete.Left)
		if err != nil {
			return nil, err
		}
		return &Aggregation{Details: details, Parent: Parent{p}}, nil
	case *syntax.RangeAggregationExpr:
		// TODO: add range interval
		// concrete.Left.Interval
		child, err := build(concrete.Left)
		if err != nil {
			return nil, err
		}
		var details AggregationDetails
		if concrete.Operation == syntax.OpRangeTypeRate {
			details = &Rate{}
		}
		return &Aggregation{Details: details, Parent: Parent{child}}, nil
	case *syntax.LogRange:
		var sb strings.Builder
		for _, m := range concrete.Left.Matchers() {
			sb.WriteString(m.String())
		}

		s, err := build(concrete.Left)
		if err != nil {
			return nil, err
		}
		scan := &Scan{Labels: sb.String()}

		if s == nil {
			return scan, nil
		}

		if concrete.Unwrap != nil {
			//	&Map{ Kind: "unwrap" }
		}

		// Push scan to the bottom.
		leaf := s
		for leaf.Child() != nil {
			leaf = leaf.Child()
		}

		leaf.SetChild(scan)
		return s, nil
	case *syntax.MatchersExpr:
		var sb strings.Builder
		for _, m := range concrete.Mts {
			sb.WriteString(m.String())
		}
		return &Scan{Labels: sb.String()}, nil
	case *syntax.PipelineExpr:
		var current Operator
		var root Operator
		for _, s := range concrete.MultiStages {
			stage, err := build(s)
			if err != nil {
				return nil, err
			}
			if current != nil {
				current.SetChild(stage)
			} else {
				root = stage
			}
			current = stage
		}
		return root, nil
	case *syntax.LineFilterExpr:
		//return &Filter{op: concrete.Op, ty: concrete.Ty, match: concrete.Match}
		return &Filter{Kind: "contains"}, nil
	case *syntax.LabelFilterExpr:
		return &Filter{Kind: "label"}, nil
	case *syntax.LabelParserExpr:
		// TODO: Not sure this is really a map operation
		return &Map{Kind: "parser"}, nil
	case *syntax.BinOpExpr:
		lhs, err := build(concrete.SampleExpr)
		if err != nil {
			return nil, err
		}

		rhs, err := build(concrete.RHS)
		if err != nil {
			return nil, err
		}

		return &Binary{Kind: concrete.Op, lhs: lhs, rhs: rhs}, nil
	default:
		return nil, fmt.Errorf("unsupported: %T(%s)", expr, expr)
	}
}
