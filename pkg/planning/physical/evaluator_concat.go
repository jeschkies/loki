package physical

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/util"
)

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
