# Query Planning v2

This package includes a prototype of a new query plan for LogQL.

* `logical` containsthe building and representation of the logical plan.
* `main.go` is a CLI tool used to analyze LogQL queries and debug a query plan.

## Example

```
$ go run main.go 'sum(rate({app="foo"} | logfmt | level=error" [1m]))' | dot -Tpng graph.png
```
