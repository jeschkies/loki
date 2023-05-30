package execution

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/iter"
	"github.com/grafana/loki/pkg/planning/plan"
)

// StepEvaluator evaluate a single step of a query.
type StepEvaluator interface {
	// while Next returns a promql.Value, the only acceptable types are Scalar and Vector.
	Next() (ok bool, ts int64, vec promql.Vector)
	// Close all resources used.
	Close() error
	// Reports any error
	Error() error
}

func NewStepEvaluator(p *plan.Plan) StepEvaluator {
	return plan.Dispatch[StepEvaluator](p.Root, &vectorEvaluatorBuilder{})
}

// TODO: Test and support case from https://github.com/grafana/loki/blob/main/pkg/logql/shardmapper_test.go#L465
type vectorEvaluatorBuilder struct{}

var _ plan.Visitor[StepEvaluator] = &vectorEvaluatorBuilder{}
var _ plan.Visitor[iter.PeekingSampleIterator] = &sampleEvaluatorBuilder{}

func (b *vectorEvaluatorBuilder) VisitAggregation(a *plan.Aggregation) StepEvaluator {
	switch a.Details.Name() {
	case "sum":
		return NewSumAggregationEvaluator(a.Grouping, plan.Dispatch[StepEvaluator](a.Child(), b))
	default:
		return nil
	}
}

func (b *vectorEvaluatorBuilder) VisitCoalescence(c *plan.Coalescence) StepEvaluator {
	e := &ConcatEvaluator{}
	for _, s := range c.Shards {
		e.evaluators = append(e.evaluators, plan.Dispatch[StepEvaluator](s, b))
	}
	return e
}

func (b *vectorEvaluatorBuilder) VisitBinary(*plan.Binary) StepEvaluator {
	return nil
}

func (b *vectorEvaluatorBuilder) VisitFilter(*plan.Filter) StepEvaluator {
	return nil
}

func (b *vectorEvaluatorBuilder) VisitMap(*plan.Map) StepEvaluator {
	return nil
}
func (b *vectorEvaluatorBuilder) VisitScan(*plan.Scan) StepEvaluator {
	return nil
}
