package physical

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/planning/logical"
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

func NewStepEvaluator(p *logical.Plan) StepEvaluator {
	return logical.Dispatch[StepEvaluator](p.Root, &evaluatorBuilder{})
}
		
// TODO: Test and support case from https://github.com/grafana/loki/blob/main/pkg/logql/shardmapper_test.go#L465
type evaluatorBuilder struct{}

var _ logical.Visitor[StepEvaluator] = &evaluatorBuilder{}

func (b *evaluatorBuilder) VisitAggregation(a *logical.Aggregation) StepEvaluator {
	switch a.Details.Name() {
	case "sum":
		return NewSumAggregationEvaluator(a.grouping, logical.Dispatch[StepEvaluator](a.Child(), b))
	default:
		return nil
	}
}

func (b *evaluatorBuilder) VisitCoalescence(c *logical.Coalescence) StepEvaluator {
	e := &ConcatEvaluator{}
	for _, s := range c.Shards {
		e.evaluators = append(e.evaluators, logical.Dispatch[StepEvaluator](s, b))
	}
	return e
}

func (b *evaluatorBuilder) VisitBinary(*logical.Binary) StepEvaluator {
	return nil
}

func (b *evaluatorBuilder) VisitFilter(*logical.Filter) StepEvaluator {
	return nil
}

func (b *evaluatorBuilder) VisitMap(*logical.Map) StepEvaluator {
	return nil
}
func (b *evaluatorBuilder) VisitScan(*logical.Scan) StepEvaluator {
	return nil
}
