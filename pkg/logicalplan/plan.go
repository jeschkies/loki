package logicalplan

import (
	"fmt"
	"strings"

	"github.com/grafana/loki/pkg/logql/syntax"
)

type Plan struct {
	Root Operator
}

func (p *Plan) String() string {
	return p.Root.String()
}

type Operator interface {
	Child() Operator
	String() string
}

type Aggregation struct {
	Kind  string
	child Operator
}

func (a *Aggregation) Child() Operator {
	return a.child
}

func (a *Aggregation) String() string {
	if a.child != nil {
		return fmt.Sprintf("Aggregation(kind=%s, %s)", a.Kind, a.child.String())
	}
	return fmt.Sprintf("Aggregation(kind=%s)", a.Kind)
}

type Projection struct {
	Child Operator
}

type Scan struct {
	Labels string
}

func (s *Scan) Child() Operator {
	return nil
}

func (s *Scan) String() string {
	return fmt.Sprintf("Scan(labels=%s)", s.Labels)
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
		return &Aggregation{Kind: concrete.Operation, child: build(concrete.Left)}
	case *syntax.RangeAggregationExpr:
		return &Aggregation{Kind: concrete.Operation, child: build(concrete.Left)}
	case *syntax.LogRange:
		var sb strings.Builder
		for _, m := range concrete.Left.Matchers() {
			sb.WriteString(m.String())
		}

		build(concrete.Left.LogPipelineExpr)

		scan := &Scan{Labels: sb.String()} 

		// The pipeline is reversed.

		return scan
		/*
		 *syntax.PipelineExpr
		 *syntax.MatchersExpr
		 *syntax.LineFilterExpr
		 *syntax.LabelParserExpr
		 *syntax.LabelFilterExpr
		 */
	}
	return nil
}
