package logicalplan

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildPlan(t *testing.T) {
	//q := `sum(rate({namespace=~"loki-prod",name=~"query-frontend"} |= "caller=metrics.go" | logfmt | org_id=~".+" | query_type=~".+" | unwrap bytes(total_bytes)[1m]))`
	q := `sum(rate({app="foo", level="warn"} | logfmt [1m])) / sum(rate({app="foo", level="error"} | logfmt [1m]))`

	plan, err := Build(q)
	require.NoError(t, err)
	require.NotNil(t, plan)
	fmt.Println(plan.String())
}
