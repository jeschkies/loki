package execution

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/logproto"
	"github.com/grafana/loki/pkg/planning/plan"
)

// StepEvaluator evaluate a single step of a query.
type StepEvaluator interface {
	// while Next returns a promql.Value, the only acceptable types are Scalar and Vector.
	Next() (ok bool, v Value)
	// Close all resources used.
	Close() error
	// Reports any error
	Error() error
}

type Value interface {
	Ts() int64
}

type Vector struct {
	ts int64
	vec promql.Vector
}

func (v Vector) Ts()  int64 {
	return v.ts
}

type Sample struct {
	ts int64
	sampl logproto.Sample // TODO: use own definition
}

func (s Sample) Ts()  int64 {
	return s.ts
}

func NewStepEvaluator(p *plan.Plan) StepEvaluator {
	return plan.Dispatch[StepEvaluator](p.Root, &evaluatorBuilder{})
}

// TODO: Test and support case from https://github.com/grafana/loki/blob/main/pkg/logql/shardmapper_test.go#L465
type evaluatorBuilder struct{}

var _ plan.Visitor[StepEvaluator] = &evaluatorBuilder{}

func (b *evaluatorBuilder) VisitAggregation(a *plan.Aggregation) StepEvaluator {
	switch a.Details.Name() {
	case "sum":
		return NewSumAggregationEvaluator(a.Grouping, plan.Dispatch[StepEvaluator](a.Child(), b))
	default:
		return nil
	}
}

func (b *evaluatorBuilder) VisitCoalescence(c *plan.Coalescence) StepEvaluator {
	e := &ConcatEvaluator{}
	for _, s := range c.Shards {
		e.evaluators = append(e.evaluators, plan.Dispatch[StepEvaluator](s, b))
	}
	return e
}

func (b *evaluatorBuilder) VisitBinary(*plan.Binary) StepEvaluator {
	return nil
}

func (b *evaluatorBuilder) VisitFilter(*plan.Filter) StepEvaluator {
	return nil
}

func (b *evaluatorBuilder) VisitMap(*plan.Map) StepEvaluator {
	return nil
}
func (b *evaluatorBuilder) VisitScan(*plan.Scan) StepEvaluator {
	return nil
}
