// Copyright 2021-2026 Zenauth Ltd.
// SPDX-License-Identifier: Apache-2.0

package planner

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"testing"

	"cel.dev/cel-go/cel"
	"github.com/ghodss/yaml"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
	"google.golang.org/protobuf/testing/protocmp"

	"google.golang.org/protobuf/encoding/protojson"

	enginev1 "github.com/cerbos/cerbos/api/genpb/cerbos/engine/v1"
	"github.com/cerbos/cerbos/internal/conditions"
	"github.com/cerbos/cerbos/internal/test"
)

//go:embed testdata/ast_build_expr.yaml
var astBuildExprBlob []byte

type (
	exOp = enginev1.PlanResourcesFilter_Expression_Operand
)

func getExpectedExpressions(t *testing.T) map[string]*exOp {
	t.Helper()

	var raw map[string]json.RawMessage
	err := yaml.Unmarshal(astBuildExprBlob, &raw)
	require.NoError(t, err)
	res := make(map[string]*exOp, len(raw))
	for k, v := range raw {
		b, err := v.MarshalJSON()
		require.NoError(t, err)
		expected := test.Parse[exOp](t, b)
		res[k] = expected
	}

	return res
}

func Test_buildExpr(t *testing.T) {
	parse := func(s string) *exprpb.Expr {
		ast, iss := conditions.StdEnv.Parse(s)
		require.Nil(t, iss, iss.Err())
		ex, err := cel.AstToParsedExpr(ast)
		require.NoError(t, err)
		return ex.Expr
	}

	for k, v := range getExpectedExpressions(t) {
		name := k
		want := v
		t.Run(name, func(t *testing.T) {
			is := require.New(t)
			acc := new(exOp)
			err := buildExpr(parse(k), acc)
			is.NoError(err)

			is.Empty(cmp.Diff(want, acc, protocmp.Transform()), "unexpected expression: %s", protojson.Format(acc))
		})
	}
}

func Test_normaliseFilterReducesContradictions(t *testing.T) {
	parse := func(s string) *exprpb.Expr {
		t.Helper()
		ast, iss := conditions.StdEnv.Parse(s)
		require.Nil(t, iss, iss.Err())
		ex, err := cel.AstToParsedExpr(ast)
		require.NoError(t, err)
		return ex.Expr
	}

	const dept = `request.resource.attr.department`

	testCases := []struct {
		name string
		cond string
		want string
	}{
		{
			name: "conjunction of two equalities on the same subject with different constants is false",
			cond: fmt.Sprintf("%s == \"secret\" && %s == \"it\"", dept, dept),
			want: "(false)",
		},
		{
			name: "negation of a contradiction simplifies the surrounding conjunction",
			cond: fmt.Sprintf("!(%s == \"secret\" && %s == \"it\") && %s == \"it\"", dept, dept, dept),
			want: fmt.Sprintf("(eq %s \"it\")", dept),
		},
		{
			name: "operand and its own negation is a contradiction",
			cond: fmt.Sprintf("!(%s == \"secret\") && %s == \"secret\"", dept, dept),
			want: "(false)",
		},
		{
			name: "equality with a different numeric constant is a contradiction",
			cond: fmt.Sprintf("request.resource.attr.size == 1 && request.resource.attr.size == 2"),
			want: "(false)",
		},
		{
			name: "equality against null and a non-null constant is a contradiction",
			cond: fmt.Sprintf("%s == null && %s == \"it\"", dept, dept),
			want: "(false)",
		},
		{
			name: "repeated identical equality is not a contradiction",
			cond: fmt.Sprintf("%s == \"it\" && %s == \"it\"", dept, dept),
			want: fmt.Sprintf("(eq %s \"it\")", dept),
		},
		{
			name: "equality on different subjects is not a contradiction",
			cond: fmt.Sprintf("%s == \"it\" && request.resource.attr.team == \"it\"", dept),
			want: fmt.Sprintf("(and (eq %s \"it\") (eq request.resource.attr.team \"it\"))", dept),
		},
		{
			name: "cross-type comparison is not treated as a contradiction",
			cond: fmt.Sprintf("%s == \"1\" && %s == 1", dept, dept),
			want: fmt.Sprintf("(and (eq %s \"1\") (eq %s 1))", dept, dept),
		},
		{
			name: "inequality on different constants is not a contradiction",
			cond: fmt.Sprintf("%s != \"it\" && %s != \"secret\"", dept, dept),
			want: fmt.Sprintf("(and (ne %s \"it\") (ne %s \"secret\"))", dept, dept),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := require.New(t)

			cond := new(exOp)
			is.NoError(buildExpr(parse(tc.cond), cond))

			filter := normaliseFilter(&enginev1.PlanResourcesFilter{
				Kind:      enginev1.PlanResourcesFilter_KIND_CONDITIONAL,
				Condition: cond,
			})

			is.Equal(tc.want, FilterToString(filter))
		})
	}
}

