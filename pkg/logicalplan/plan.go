package logicalplan

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	w.WriteString("\n")

	visitor := &Graphviz{writer: w}
	p.Root.Accept(visitor)

	w.WriteString("}")
}

type Graphviz struct {
	writer io.StringWriter
}

func (g *Graphviz) visitAggregation(a *Aggregation) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Aggregation:%s\n%s"];`, a.GetID(), a.Details.Name(), a.Details.Parameters()))
	g.writer.WriteString("\n")
	if a.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, a.Child().GetID(), a.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitBinary(b *Binary) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Binary:%s"];`, b.GetID(), b.Kind))
	g.writer.WriteString("\n")
	if b.lhs != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.lhs.GetID(), b.GetID()))
		g.writer.WriteString("\n")
	}
	if b.rhs != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.rhs.GetID(), b.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitFilter(f *Filter) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Filter:%s"];`, f.GetID(), f.Kind))
	g.writer.WriteString("\n")
	if f.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, f.Child().GetID(), f.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitMap(m *Map) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Map:%s"];`, m.GetID(), m.Kind))
	g.writer.WriteString("\n")
	if m.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, m.Child().GetID(), m.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitScan(s *Scan) {
	labels := strconv.Quote(s.Labels())
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Scan\nLabels: %s"];`, s.GetID(), labels[1:len(labels)-1]))
	g.writer.WriteString("\n")
	if s.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, s.Child().GetID(), s.GetID()))
		g.writer.WriteString("\n")
	}
}

type Operator interface {
	GetID() string
	Child() Operator
	SetChild(Operator)
	String() string
	Accept(Visitor)
}

type Visitor interface {
	visitAggregation(*Aggregation)
	visitBinary(*Binary)
	visitFilter(*Filter)
	visitMap(*Map)
	visitScan(*Scan)
}

type Parent struct {
	child Operator
}

type ID struct {
	uuid.UUID
}

func NewID() ID {
	return ID{uuid.New()}
}

func (i ID) GetID() string {
	return i.UUID.String()
}

func (p *Parent) Child() Operator {
	return p.child
}

func (p *Parent) SetChild(o Operator) {
	p.child = o
}

type AggregationDetails interface {
	Name() string
	Parameters() string
}

type Aggregation struct {
	Details AggregationDetails
	ID
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

type Sum struct {
	ID
}

func (s *Sum) Name() string {
	return "sum"
}

func (s *Sum) Parameters() string {
	return ""
}

type Rate struct {
	ID
	Interval time.Duration
}

func (s *Rate) Name() string {
	return "rate"
}

func (s *Rate) Parameters() string {
	return fmt.Sprintf("interval:%s", s.Interval)
}

type Filter struct {
	ID
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
	ID
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
	ID
	Kind string
	lhs  Operator
	rhs  Operator
}

func (b *Binary) String() string {
	return fmt.Sprintf("Binary(kind=%s, %s, %s)", b.Kind, b.lhs, b.rhs)
}

func (b *Binary) Accept(v Visitor) {
	v.visitBinary(b)
	if b.lhs != nil {
		b.lhs.Accept(v)
	}
	if b.rhs != nil {
		b.rhs.Accept(v)
	}
}

func (b *Binary) Child() Operator {
	return nil // TODO
}

func (b *Binary) SetChild(o Operator) {
	// TODO
}

type Scan struct {
	ID
	Matchers []*labels.Matcher
}

func (s *Scan) Child() Operator {
	return nil
}

func (s *Scan) SetChild(_ Operator) {}

func (s *Scan) String() string {
	return fmt.Sprintf("Scan(labels=%s)", s.Labels())
}

func (s *Scan) Labels() string {
	var sb strings.Builder
	sb.WriteString("{")
	for i, m := range s.Matchers {
		sb.WriteString(m.String())
		if i+1 != len(s.Matchers) {
			sb.WriteString(", ")
		}
	}
	sb.WriteString("}")
	return sb.String()
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
		return &Aggregation{ID: NewID(), Details: details, Parent: Parent{p}}, nil
	case *syntax.RangeAggregationExpr:
		child, err := build(concrete.Left)
		if err != nil {
			return nil, err
		}
		var details AggregationDetails
		if concrete.Operation == syntax.OpRangeTypeRate {
			details = &Rate{Interval: concrete.Left.Interval}
		}
		return &Aggregation{ID: NewID(), Details: details, Parent: Parent{child}}, nil
	case *syntax.LogRange:

		s, err := build(concrete.Left)
		if err != nil {
			return nil, err
		}
		scan := &Scan{ID: NewID(), Matchers: concrete.Left.Matchers()}

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
		return &Scan{ID: NewID(), Matchers: concrete.Mts}, nil
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
		return &Filter{ID: NewID(), Kind: "contains"}, nil
	case *syntax.LabelFilterExpr:
		return &Filter{ID: NewID(), Kind: "label"}, nil
	case *syntax.LabelParserExpr:
		// TODO: Not sure this is really a map operation
		return &Map{ID: NewID(), Kind: "parser"}, nil
	case *syntax.BinOpExpr:
		lhs, err := build(concrete.SampleExpr)
		if err != nil {
			return nil, err
		}

		rhs, err := build(concrete.RHS)
		if err != nil {
			return nil, err
		}

		return &Binary{ID: NewID(), Kind: concrete.Op, lhs: lhs, rhs: rhs}, nil
	default:
		return nil, fmt.Errorf("unsupported: %T(%s)", expr, expr)
	}
}
