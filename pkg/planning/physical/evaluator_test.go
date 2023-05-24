package physical

import (
	"testing"

	"github.com/grafana/loki/pkg/planning/logical"
)

func TestBuilder(t *testing.T) {
	var b logical.Visitor[StepEvaluator] = &Builder{}
	b.VisitAggregation(nil)
}
