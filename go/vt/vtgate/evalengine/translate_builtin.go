/*
Copyright 2023 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package evalengine

import (
	"fmt"
	"strings"

	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vterrors"
)

type argError string

func (err argError) Error() string {
	return fmt.Sprintf("Incorrect parameter count in the call to native function '%s'", string(err))
}

func (ast *astCompiler) translateFuncArgs(fnargs []sqlparser.Expr) ([]Expr, error) {
	var args TupleExpr
	for _, expr := range fnargs {
		convertedExpr, err := ast.translateExpr(expr)
		if err != nil {
			return nil, err
		}
		args = append(args, convertedExpr)
	}
	return args, nil
}

func (ast *astCompiler) translateFuncExpr(fn *sqlparser.FuncExpr) (Expr, error) {
	var args TupleExpr
	for _, expr := range fn.Exprs {
		aliased, ok := expr.(*sqlparser.AliasedExpr)
		if !ok {
			return nil, translateExprNotSupported(fn)
		}
		convertedExpr, err := ast.translateExpr(aliased.Expr)
		if err != nil {
			return nil, err
		}
		args = append(args, convertedExpr)
	}

	method := fn.Name.Lowered()
	call := CallExpr{Arguments: args, Method: method}

	switch method {
	case "isnull":
		return builtinIsNullRewrite(args)
	case "ifnull":
		return builtinIfNullRewrite(args)
	case "nullif":
		return builtinNullIfRewrite(args)
	case "coalesce":
		if len(args) == 0 {
			return nil, argError(method)
		}
		return &builtinCoalesce{CallExpr: call}, nil
	case "greatest":
		if len(args) == 0 {
			return nil, argError(method)
		}
		return &builtinMultiComparison{CallExpr: call, cmp: 1}, nil
	case "least":
		if len(args) == 0 {
			return nil, argError(method)
		}
		return &builtinMultiComparison{CallExpr: call, cmp: -1}, nil
	case "collation":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinCollation{CallExpr: call}, nil
	case "bit_count":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinBitCount{CallExpr: call}, nil
	case "hex":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinHex{CallExpr: call}, nil
	case "ceil", "ceiling":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinCeil{CallExpr: call}, nil
	case "lower", "lcase":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinChangeCase{CallExpr: call, upcase: false}, nil
	case "upper", "ucase":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinChangeCase{CallExpr: call, upcase: true}, nil
	case "char_length", "character_length":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinCharLength{CallExpr: call}, nil
	case "length", "octet_length":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinLength{CallExpr: call}, nil
	case "bit_length":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinBitLength{CallExpr: call}, nil
	case "ascii":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinASCII{CallExpr: call}, nil
	case "repeat":
		if len(args) != 2 {
			return nil, argError(method)
		}
		return &builtinRepeat{CallExpr: call}, nil
	case "from_base64":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinFromBase64{CallExpr: call}, nil
	case "to_base64":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinToBase64{CallExpr: call}, nil
	case "json_depth":
		if len(args) != 1 {
			return nil, argError(method)
		}
		return &builtinJSONDepth{CallExpr: call}, nil
	case "json_length":
		switch len(args) {
		case 1, 2:
			return &builtinJSONLength{CallExpr: call}, nil
		default:
			return nil, argError(method)
		}
	default:
		return nil, translateExprNotSupported(fn)
	}
}

func (ast *astCompiler) translateCallable(call sqlparser.Callable) (Expr, error) {
	switch call := call.(type) {
	case *sqlparser.FuncExpr:
		return ast.translateFuncExpr(call)

	case *sqlparser.ConvertExpr:
		return ast.translateConvertExpr(call.Expr, call.Type)

	case *sqlparser.ConvertUsingExpr:
		return ast.translateConvertUsingExpr(call)

	case *sqlparser.WeightStringFuncExpr:
		var ws builtinWeightString
		var err error

		ws.String, err = ast.translateExpr(call.Expr)
		if err != nil {
			return nil, err
		}
		if call.As != nil {
			ws.Cast = strings.ToLower(call.As.Type)
			ws.Len, ws.HasLen, err = ast.translateIntegral(call.As.Length)
			if err != nil {
				return nil, err
			}
		}
		return &ws, nil

	case *sqlparser.JSONExtractExpr:
		args, err := ast.translateFuncArgs(append([]sqlparser.Expr{call.JSONDoc}, call.PathList...))
		if err != nil {
			return nil, err
		}
		return &builtinJSONExtract{
			CallExpr: CallExpr{
				Arguments: args,
				Method:    "JSON_EXTRACT",
			},
		}, nil

	case *sqlparser.JSONUnquoteExpr:
		arg, err := ast.translateExpr(call.JSONValue)
		if err != nil {
			return nil, err
		}
		return &builtinJSONUnquote{
			CallExpr: CallExpr{
				Arguments: []Expr{arg},
				Method:    "JSON_UNQUOTE",
			},
		}, nil

	case *sqlparser.JSONObjectExpr:
		var args []Expr
		for _, param := range call.Params {
			key, err := ast.translateExpr(param.Key)
			if err != nil {
				return nil, err
			}
			val, err := ast.translateExpr(param.Value)
			if err != nil {
				return nil, err
			}
			args = append(args, key, val)
		}
		return &builtinJSONObject{
			CallExpr: CallExpr{
				Arguments: args,
				Method:    "JSON_OBJECT",
			},
		}, nil

	case *sqlparser.JSONArrayExpr:
		args, err := ast.translateFuncArgs(call.Params)
		if err != nil {
			return nil, err
		}
		return &builtinJSONArray{CallExpr: CallExpr{
			Arguments: args,
			Method:    "JSON_ARRAY",
		}}, nil

	case *sqlparser.JSONContainsPathExpr:
		exprs := []sqlparser.Expr{call.JSONDoc, call.OneOrAll}
		exprs = append(exprs, call.PathList...)
		args, err := ast.translateFuncArgs(exprs)
		if err != nil {
			return nil, err
		}
		return &builtinJSONContainsPath{CallExpr: CallExpr{
			Arguments: args,
			Method:    "JSON_CONTAINS_PATH",
		}}, nil

	case *sqlparser.JSONKeysExpr:
		var args []Expr
		doc, err := ast.translateExpr(call.JSONDoc)
		if err != nil {
			return nil, err
		}
		args = append(args, doc)

		if call.Path != nil {
			path, err := ast.translateExpr(call.Path)
			if err != nil {
				return nil, err
			}
			args = append(args, path)
		}

		return &builtinJSONKeys{CallExpr: CallExpr{
			Arguments: args,
			Method:    "JSON_KEYS",
		}}, nil

	default:
		return nil, translateExprNotSupported(call)
	}
}

func builtinJSONExtractUnquoteRewrite(left Expr, right Expr) (Expr, error) {
	extract, err := builtinJSONExtractRewrite(left, right)
	if err != nil {
		return nil, err
	}
	return &builtinJSONUnquote{
		CallExpr: CallExpr{
			Arguments: []Expr{extract},
			Method:    "JSON_UNQUOTE",
		},
	}, nil
}

func builtinJSONExtractRewrite(left Expr, right Expr) (Expr, error) {
	if _, ok := left.(*Column); !ok {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "lhs of a JSON extract operator must be a column")
	}
	return &builtinJSONExtract{
		CallExpr: CallExpr{
			Arguments: []Expr{left, right},
			Method:    "JSON_EXTRACT",
		},
	}, nil
}

func builtinIsNullRewrite(args []Expr) (Expr, error) {
	if len(args) != 1 {
		return nil, argError("ISNULL")
	}
	return &IsExpr{
		UnaryExpr: UnaryExpr{args[0]},
		Op:        sqlparser.IsNullOp,
		Check:     func(e eval) bool { return e == nil },
	}, nil
}

func builtinIfNullRewrite(args []Expr) (Expr, error) {
	if len(args) != 2 {
		return nil, argError("IFNULL")
	}
	var result CaseExpr
	result.cases = append(result.cases, WhenThen{
		when: &IsExpr{
			UnaryExpr: UnaryExpr{args[0]},
			Op:        sqlparser.IsNullOp,
			Check: func(e eval) bool {
				return e == nil
			},
		},
		then: args[1],
	})
	result.Else = args[0]
	return &result, nil
}

func builtinNullIfRewrite(args []Expr) (Expr, error) {
	if len(args) != 2 {
		return nil, argError("NULLIF")
	}
	var result CaseExpr
	result.cases = append(result.cases, WhenThen{
		when: &ComparisonExpr{
			BinaryExpr: BinaryExpr{
				Left:  args[0],
				Right: args[1],
			},
			Op: compareEQ{},
		},
		then: NullExpr,
	})
	result.Else = args[0]
	return &result, nil
}
