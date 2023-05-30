package execution

import (
	"github.com/grafana/loki/pkg/util"
)

// ConcatEvaluator joins multiple StepEvaluators.
// Contract: They must be of identical start, end, and step values.
type ConcatEvaluator struct {
	evaluators []StepEvaluator
}

var _ StepEvaluator = &ConcatEvaluator{}

func (e *ConcatEvaluator) Next() (bool, Value) {
	var v Vector
	var ok bool
	var next Value
	for _, eval := range e.evaluators {
		ok, next = eval.Next()
		cur := next.(Vector).vec
		v.vec = append(v.vec, cur...)
	}
	return ok, v
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
