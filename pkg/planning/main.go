//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/grafana/loki/pkg/planning/logical"
)

// go run pkg/logicalplan/main.go 'sum(...)' | dot -Tsvg > output.svg
func main() {
	query := os.Args[1]

	p, err := logical.NewPlan(query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not build plan: %s", err)
		os.Exit(1)
	}

	logical.Dispatch[logical.Unit](p.Root, logical.NewRegexpOptimizer())
	p = logical.ShardAggregations(p, logical.ConstantShards(4))

	p.Graphviz(os.Stdout)
}
