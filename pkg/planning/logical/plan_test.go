package logical

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanBuilding(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
	}{
		{
			"long pipeline",
			`sum(rate({namespace=~"loki-prod",name=~"query-frontend"} |= "caller=metrics.go" | logfmt | org_id=~".+" | query_type=~".+" | unwrap bytes(total_bytes)[1m]))`,
		},
		{
			"sum / sum",
			`sum(rate({app="foo", level="warn"}[1m])) / sum(rate({app="foo", level="error"} | logfmt [1m]))`,
		},
		{
			"simple sum",
			`sum(rate({app="foo", level="warn"}[1m]))`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := NewPlan(tt.query)
			require.NoError(t, err)
			require.NotNil(t, plan)

			// Test invariants
			for _, o := range plan.Leafs() {
				require.IsType(t, &Scan{}, o)
			}
			//fmt.Println(plan.String())
		})
	}

}
