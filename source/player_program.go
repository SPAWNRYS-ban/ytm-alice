package main

import (
	"errors"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

func ungroupPlayer(value js.IExpr) js.IExpr {
	for {
		group, ok := value.(*js.GroupExpr)
		if !ok {
			return value
		}
		value = group.X
	}
}

func playerAssignment(op js.TokenType) bool {
	switch op {
	case js.EqToken, js.AddEqToken, js.SubEqToken, js.MulEqToken, js.DivEqToken,
		js.ModEqToken, js.ExpEqToken, js.LtLtEqToken, js.GtGtEqToken, js.GtGtGtEqToken,
		js.BitAndEqToken, js.BitOrEqToken, js.BitXorEqToken, js.AndEqToken, js.OrEqToken, js.NullishEqToken:
		return true
	}
	return false
}

// Mirror the yt-dlp/ejs 0.8.0 preprocessing with a native parser. Meriyah's
// JavaScript AST takes over 25s on a Station Micro. This only parses text;
// all execution stays in the memory- and time-bounded isolated helper.
func preprocessPlayer(source string) (string, error) {
	invalid := errors.New("YouTube изменил структуру декодера аудиопотока")
	if len(source) > 4<<20 {
		return "", invalid
	}
	program, err := js.Parse(parse.NewInputString(source), js.Options{})
	if err != nil {
		return "", invalid
	}
	var body *js.BlockStmt
	skip := 0
	if len(program.List) == 1 || len(program.List) == 2 {
		if expression, ok := program.List[len(program.List)-1].(*js.ExprStmt); ok {
			if call, ok := ungroupPlayer(expression.Value).(*js.CallExpr); ok {
				target := ungroupPlayer(call.X)
				if len(program.List) == 1 {
					switch member := target.(type) {
					case *js.DotExpr:
						target = ungroupPlayer(member.X)
					case *js.IndexExpr:
						target = ungroupPlayer(member.X)
					default:
						target = nil
					}
				} else {
					skip = 1
				}
				if function, ok := target.(*js.FuncDecl); ok {
					body = &function.Body
				}
			}
		}
	}
	if body == nil || len(body.List) < skip {
		return "", invalid
	}
	names := []string{}
	candidate := func(name js.IExpr, function *js.FuncDecl) {
		if function.Async {
			return
		}
		for _, statement := range function.Body.List {
			expression, ok := statement.(*js.ExprStmt)
			if !ok {
				continue
			}
			call, ok := ungroupPlayer(expression.Value).(*js.CallExpr)
			if !ok || call.Optional || len(call.Args.List) != 2 {
				continue
			}
			var object js.IExpr
			switch member := ungroupPlayer(call.X).(type) {
			case *js.DotExpr:
				if member.Optional {
					continue
				}
				object = member.X
			case *js.IndexExpr:
				if member.Optional {
					continue
				}
				object = member.X
			default:
				continue
			}
			if _, ok := ungroupPlayer(object).(*js.Var); !ok {
				continue
			}
			first, ok := call.Args.List[0].Value.(*js.LiteralExpr)
			if !ok || (string(first.Data) != "'alr'" && string(first.Data) != `"alr"`) {
				continue
			}
			second, ok := call.Args.List[1].Value.(*js.LiteralExpr)
			if !ok || (string(second.Data) != "'yes'" && string(second.Data) != `"yes"`) {
				continue
			}
			var text strings.Builder
			name.JS(&text)
			names = append(names, text.String())
			return
		}
	}
	kept := make([]js.IStmt, 0, len(body.List))
	for _, statement := range body.List[skip:] {
		switch value := statement.(type) {
		case *js.FuncDecl:
			if value.Name != nil {
				candidate(value.Name, value)
			}
		case *js.VarDecl:
			for _, binding := range value.List {
				if function, ok := binding.Default.(*js.FuncDecl); ok {
					if name, ok := binding.Binding.(*js.Var); ok {
						candidate(name, function)
					}
				}
			}
		case *js.ExprStmt:
			switch expression := ungroupPlayer(value.Value).(type) {
			case *js.BinaryExpr:
				if !playerAssignment(expression.Op) {
					continue
				}
				if function, ok := ungroupPlayer(expression.Y).(*js.FuncDecl); ok && expression.Op == js.EqToken {
					switch ungroupPlayer(expression.X).(type) {
					case *js.Var, *js.DotExpr, *js.IndexExpr:
						candidate(expression.X, function)
					}
				}
			case *js.LiteralExpr:
			default:
				continue // Do not execute the native player's startup calls.
			}
		}
		kept = append(kept, statement)
	}
	if len(names) == 0 {
		return "", invalid
	}
	body.List = kept
	var code strings.Builder
	for _, kind := range []string{"sig", "n"} {
		code.WriteString("_result." + kind + " = (_input) => {const results=new Set();for(const generate of [")
		for _, name := range names {
			code.WriteString("()=>{const url=(" + name + ")('https://youtube.com/watch?v=yt-dlp-wins','s',")
			if kind == "sig" {
				code.WriteString("encodeURIComponent(_input)")
			} else {
				code.WriteString("undefined")
			}
			code.WriteString(");url.set('n',")
			if kind == "n" {
				code.WriteString("_input")
			} else {
				code.WriteString("undefined")
			}
			code.WriteString(");const proto=Object.getPrototypeOf(url);for(const key of Object.keys(proto).concat(Object.getOwnPropertyNames(proto))){if(!['constructor','set','get','clone'].includes(key)){url[key]();break;}}")
			if kind == "sig" {
				code.WriteString("const s=url.get('s');return s?decodeURIComponent(s):null;")
			} else {
				code.WriteString("return url.get('n')??null;")
			}
			code.WriteString("},")
		}
		code.WriteString("]){try{results.add(generate());}catch(e){}}if(results.size!==1){throw new Error('player transform failed');}return results.values().next().value;};\n")
	}
	generated, err := js.Parse(parse.NewInputString(code.String()), js.Options{})
	if err != nil {
		return "", invalid
	}
	body.List = append(body.List, generated.List...)
	var output strings.Builder
	output.WriteString(playerPrelude)
	program.JS(&output)
	return output.String(), nil
}

// Browser globals supplied by the upstream EJS preprocessing stage.
const playerPrelude = `
if(typeof globalThis.XMLHttpRequest==='undefined'){globalThis.XMLHttpRequest={prototype:{}};}
if(typeof URL==='undefined'){globalThis.location={hash:'',host:'www.youtube.com',hostname:'www.youtube.com',href:'https://www.youtube.com/watch?v=yt-dlp-wins',origin:'https://www.youtube.com',password:'',pathname:'/watch',port:'',protocol:'https:',search:'?v=yt-dlp-wins',username:''};}else{globalThis.location=new URL('https://www.youtube.com/watch?v=yt-dlp-wins');}
if(typeof globalThis.document==='undefined'){globalThis.document=Object.create(null);}
if(typeof globalThis.navigator==='undefined'){globalThis.navigator=Object.create(null);}
if(typeof globalThis.self==='undefined'){globalThis.self=globalThis;}
if(typeof globalThis.window==='undefined'){globalThis.window=globalThis;}
`
