package database

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"modernc.org/sqlite"

	"github.com/mikluko/sniffarr/internal/sketch"
)

// The SQL functions over peer sketches, available on every connection:
// hll_union(a, b) and the aggregate hll_union_agg(sketch) return the union of
// their sketches, NULL for none, and hll_card(sketch) its cardinality, 0 for
// NULL.
func init() {
	for name, impl := range map[string]*sqlite.FunctionImpl{
		"hll_union":     {NArgs: 2, Deterministic: true, Scalar: hllUnion},
		"hll_card":      {NArgs: 1, Deterministic: true, Scalar: hllCard},
		"hll_union_agg": {NArgs: 1, MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &hllUnionAgg{}, nil }},
	} {
		if err := sqlite.RegisterFunction(name, impl); err != nil {
			panic(fmt.Errorf("register %s: %w", name, err))
		}
	}
}

func hllUnion(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	a, b := args[0], args[1]
	if a == nil {
		return b, nil
	}
	if b == nil {
		return a, nil
	}
	sa, err := parseSketch(a)
	if err != nil {
		return nil, err
	}
	sb, err := parseSketch(b)
	if err != nil {
		return nil, err
	}
	if err := sa.Union(sb); err != nil {
		return nil, err
	}
	return sa.Bytes(), nil
}

func hllCard(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return int64(0), nil
	}
	s, err := parseSketch(args[0])
	if err != nil {
		return nil, err
	}
	return int64(s.Cardinality()), nil
}

// hllUnionAgg unions the sketches stepped through; it is not a window
// function.
type hllUnionAgg struct {
	acc *sketch.Sketch
}

func (a *hllUnionAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[0] == nil {
		return nil
	}
	s, err := parseSketch(args[0])
	if err != nil {
		return err
	}
	if a.acc == nil {
		a.acc = &s
		return nil
	}
	return a.acc.Union(s)
}

func (a *hllUnionAgg) WindowInverse(*sqlite.FunctionContext, []driver.Value) error {
	return errors.New("hll_union_agg is not a window function")
}

func (a *hllUnionAgg) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if a.acc == nil {
		return nil, nil
	}
	return a.acc.Bytes(), nil
}

func (a *hllUnionAgg) Final(*sqlite.FunctionContext) {}

func parseSketch(v driver.Value) (sketch.Sketch, error) {
	b, ok := v.([]byte)
	if !ok {
		return sketch.Sketch{}, fmt.Errorf("sketch of type %T, want a blob", v)
	}
	return sketch.Parse(b)
}
