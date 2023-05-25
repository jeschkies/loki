package physical

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/pkg/planning/logical"
)

func TestBuilder(t *testing.T) {
	q := `sum(rate({foo="bar"}[5m]))`

	p, err := logical.NewPlan(q)
	require.NoError(t, err)

	NewStepEvaluator(p)
}
