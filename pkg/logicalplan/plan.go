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

	c := p.Root
	i := 0
	for c != nil {
		w.WriteString(fmt.Sprintf(`%d [label="%T"];`, i, c))
		if c.Child() != nil {
			w.WriteString(fmt.Sprintf(`%d -> %d;`, i+1, i))
		}
		i++
		c = c.Child()
	}

	w.WriteString("}")
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
}

func Build(query string) (*Plan, error) {
	expr, err := syntax.ParseExpr(query)
	if err != nil {
		return nil, err
	}

	plan := &Plan{Root: build(expr)}
	return plan, nil
}

func build(expr syntax.Expr) Operator {
	switch concrete := expr.(type) {
	case *syntax.VectorAggregationExpr:
		var details AggregationDetails
		if concrete.Operation == syntax.OpTypeSum {
			details = &Sum{}
		}
		return &Aggregation{Details: details, Parent: Parent{build(concrete.Left)}}
	case *syntax.RangeAggregationExpr:
		// TODO: add range interval
		// concrete.Left.Interval
		child := build(concrete.Left)
		var details AggregationDetails
		if concrete.Operation == syntax.OpRangeTypeRate {
			details = &Rate{}
		}
		return &Aggregation{Details: details, Parent: Parent{child}}
	case *syntax.LogRange:
		var sb strings.Builder
		for _, m := range concrete.Left.Matchers() {
			sb.WriteString(m.String())
		}

		s := build(concrete.Left)
		scan := &Scan{Labels: sb.String()}

		if s == nil {
			return scan
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
		return s
	case *syntax.PipelineExpr:
		var current Operator
		var root Operator
		for _, s := range concrete.MultiStages {
			stage := build(s)
			if current != nil {
				current.SetChild(stage)
			} else {
				root = stage
			}
			current = stage
		}
		return root
	case *syntax.LineFilterExpr:
		//return &Filter{op: concrete.Op, ty: concrete.Ty, match: concrete.Match}
		return &Filter{Kind: "contains"}
	case *syntax.LabelFilterExpr:
		return &Filter{Kind: "label"}
	case *syntax.LabelParserExpr:
		// TODO: Not sure this is really a map operation
		return &Map{Kind: "parser"}
	default:
		fmt.Printf("unsupported: %T(%s)\n", expr, expr)
	}
	return nil
}
