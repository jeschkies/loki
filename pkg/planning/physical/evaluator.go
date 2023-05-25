package physical

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/planning/logical"
	"github.com/grafana/loki/pkg/util"
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

// ConcatEvaluator joins multiple StepEvaluators.
// Contract: They must be of identical start, end, and step values.
type ConcatEvaluator struct {
	evaluators []StepEvaluator
}

var _ StepEvaluator = &ConcatEvaluator{}

func (e *ConcatEvaluator) Next() (ok bool, ts int64, vec promql.Vector) {
	var cur promql.Vector
	for _, eval := range e.evaluators {
		ok, ts, cur = eval.Next()
		vec = append(vec, cur...)
	}
	return ok, ts, vec
}

func (e *ConcatEvaluator) Close() (lastErr error) {
	for _, eval := range e.evaluators {
		if err := eval.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (e *ConcatEvaluator) Error() error {
	var errs []error
	for _, eval := range e.evaluators {
		if err := eval.Error(); err != nil {
			errs = append(errs, err)
		}
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return util.MultiError(errs)
	}
}

type Builder struct{}

var _ logical.Visitor[StepEvaluator] = &Builder{}

func (b *Builder) VisitAggregation(*logical.Aggregation) StepEvaluator {
	return nil
}

func (b *Builder) VisitCoalescence(c *logical.Coalescence) StepEvaluator {
	e := &ConcatEvaluator{}
	for _, s := range c.Shards {
		e.evaluators = append(e.evaluators, logical.Dispatch[StepEvaluator](s, b))
	}
	return e
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
