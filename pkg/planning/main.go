//go:build ignore

package main

import (
	"os"

	"github.com/grafana/loki/pkg/planning/logical"
)

// go run pkg/logicalplan/main.go 'sum(...)' | dot -Tsvg > output.svg
func main() {
	query := os.Args[1]

	p, _ := logical.NewPlan(query)
	p.Graphviz(os.Stdout)
}
