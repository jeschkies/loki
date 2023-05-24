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
	return logical.Dispatch[StepEvaluator](p.Root, &Builder{}) 
}

type Builder struct{}

var _ logical.Visitor[StepEvaluator] = &Builder{}

func (b *Builder) VisitAggregation(*logical.Aggregation) StepEvaluator {
	return nil
}

func (b *Builder) VisitCoalescence(*logical.Coalescence) StepEvaluator {
	return nil
}

func (b *Builder) VisitBinary(*logical.Binary) StepEvaluator {
	return nil
}

func (b *Builder) VisitFilter(*logical.Filter) StepEvaluator {
	return nil
}

func (b *Builder) VisitMap(*logical.Map) StepEvaluator {
	return nil
}
func (b *Builder) VisitScan(*logical.Scan) StepEvaluator {
	return nil
}
