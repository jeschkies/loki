package execution

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/pkg/planning/plan"
)

func TestBuilder(t *testing.T) {
	q := `sum(rate({foo="bar"}[5m]))`

	p, err := plan.NewPlan(q)
	require.NoError(t, err)

	NewStepEvaluator(p)
}
