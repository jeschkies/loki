package logical

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/pkg/logql/syntax"
)

type Plan struct {
	Root Operator
}

type Operator interface {
	GetID() string
	Child() Operator
	Accept(Visitor)

	// DeepClone makes a copy of the Operator and all its children and
	// updates their ids.
	// TODO: this could probably be handled by a visitor again.
	DeepClone() Operator

	// TODO: these are only used to settng Scan.
	SetChild(Operator)
	Leaf() Operator
}

// Type checks
var _ Operator = &Aggregation{}
var _ Operator = &Coalescence{}
var _ Operator = &Filter{}
var _ Operator = &Map{}
var _ Operator = &Scan{}

func (p *Plan) String() string {
	var w strings.Builder
	p.Root.Accept(NewStringer(&w))
	return w.String()
}

func (p *Plan) Replace(oldOperator, newOperator Operator) {
	current := p.Root
	if current.GetID() == oldOperator.GetID() {
		p.Root = newOperator
	}

	for current.Child() != nil {
		if current.Child().GetID() == oldOperator.GetID() {
			current.SetChild(newOperator)
			return
		}
		current = current.Child()
	}
}

// Leafs returns all leaf nodes.
func (p *Plan) Leafs() []Operator {
	visitor := &LeafAccumulator{}
	p.Root.Accept(visitor)
	return visitor.Leafs
}

// Graphviz writes the logical plan in dot language.
// TODO: Define iteration pattern.
func (p *Plan) Graphviz(w io.StringWriter) {
	w.WriteString(`digraph { node [shape=rect];rankdir="BT";`)
	w.WriteString("\n")

	p.Root.Accept(NewGraphviz(w))

	w.WriteString("}")
}

// Visitor see https://www.lihaoyi.com/post/ZeroOverheadTreeProcessingwiththeVisitorPattern.html
type Visitor interface {
	visitAggregation(*Aggregation)
	visitCoalescence(*Coalescence)
	visitBinary(*Binary)
	visitFilter(*Filter)
	visitMap(*Map)
	visitScan(*Scan)
}

type Parent struct {
	child Operator
}

func (p *Parent) Leaf() Operator {
	// TODO: ignoring parent itself for now
	c := p.child
	if c == nil {
		return nil
	}

	return c.Leaf()
}

func (p *Parent) Child() Operator {
	return p.child
}

func (p *Parent) SetChild(o Operator) {
	p.child = o
}

func (p *Parent) DeepClone() Parent {
	return Parent{child: p.child.DeepClone()}
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

type AggregationDetails interface {
	Name() string
	Parameters() string
}

type Aggregation struct {
	Details AggregationDetails
	ID
	Parent
}

func (a *Aggregation) Accept(v Visitor) {
	v.visitAggregation(a)
	if a.Child() != nil {
		a.Child().Accept(v)
	}
}

func (a *Aggregation) DeepClone() Operator {
	return &Aggregation{ID: NewID(), Details: a.Details, Parent: a.Parent.DeepClone()}
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

type Coalescence struct {
	ID
	shards []Operator
}

func NewCoalescene() *Coalescence {
	return &Coalescence{ID: NewID()}
}

func (c *Coalescence) Accept(v Visitor) {
	v.visitCoalescence(c)

	for _, s := range c.shards {
		s.Accept(v)
	}
}

func (c *Coalescence) Child() Operator {
	return nil // TODO: we need Children instead of child
}

func (c *Coalescence) SetChild(o Operator) {
	// TODO: remove this method. It's only used for scan.
}

func (c *Coalescence) Leaf() Operator {
	// TODO: figure out what to return
	return nil
}

func (c *Coalescence) DeepClone() Operator {
	var shards []Operator
	for _, s := range c.shards {
		shards = append(shards, s.DeepClone())
	}
	return &Coalescence{ID: NewID(), shards: shards}
}

type Filter struct {
	ID
	op    string
	match string
	ty    labels.MatchType
	Kind  string
	Parent
}

func NewFilter(id ID, kind string, expr *syntax.LineFilterExpr) *Filter {
	return &Filter{ID: id, Kind: kind, op: expr.Op, ty: expr.Ty, match: expr.Match}
}

func (f *Filter) Accept(v Visitor) {
	v.visitFilter(f)
	if f.Child() != nil {
		f.Child().Accept(v)
	}
}

func (f *Filter) DeepClone() Operator {
	return &Filter{
		ID:     NewID(),
		Kind:   f.Kind,
		op:     f.op,
		ty:     f.ty,
		match:  f.match,
		Parent: f.Parent.DeepClone(),
	}
}

type Map struct {
	ID
	Kind string
	Parent
}

func (m *Map) Accept(v Visitor) {
	v.visitMap(m)
	if m.Child() != nil {
		m.Child().Accept(v)
	}
}

func (m *Map) DeepClone() Operator {
	return &Map{ID: NewID(), Kind: m.Kind, Parent: m.Parent.DeepClone()}
}

type Binary struct {
	ID
	Kind string
	lhs  Operator
	rhs  Operator
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

func (b *Binary) Leaf() Operator {
	return nil // TODO
}

func (b *Binary) SetChild(o Operator) {
	// TODO
}

func (b *Binary) DeepClone() Operator {
	return &Binary{ID: NewID(), Kind: b.Kind, lhs: b.lhs.DeepClone(), rhs: b.rhs.DeepClone()}
}

type ShardAnnotation struct {
	Shard int
	Of    int
}

type Scan struct {
	ID
	Matchers []*labels.Matcher
	shard    *ShardAnnotation
}

func (s *Scan) Child() Operator {
	return nil
}

func (s *Scan) SetChild(_ Operator) {}

func (s *Scan) Leaf() Operator {
	return nil
}

func (s *Scan) DeepClone() Operator {
	var matchers []*labels.Matcher
	for _, m := range s.Matchers {
		matchers = append(matchers, labels.MustNewMatcher(m.Type, m.Name, m.Value))
	}
	cloned := &Scan{ID: NewID(), Matchers: matchers}
	if s.shard != nil {
		shard := *s.shard
		cloned.shard = &shard
	}
	return cloned
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

// NewPlan constructs a logical plan from the query or an error.
func NewPlan(query string) (*Plan, error) {
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

// TODO: use visitor to build the plan. See https://www.lihaoyi.com/post/ZeroOverheadTreeProcessingwiththeVisitorPattern.html#streaming-sources
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
		return build(concrete.Left)
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
		scan := &Scan{ID: NewID(), Matchers: concrete.Left.Matchers()}

		if root == nil {
			return scan, nil
		}

		l := root.Leaf()
		if l != nil {
			l.SetChild(scan)
		} else {
			root.SetChild(scan)
		}

		return root, nil
	case *syntax.LineFilterExpr:
		return NewFilter(NewID(), "line", concrete), nil
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
