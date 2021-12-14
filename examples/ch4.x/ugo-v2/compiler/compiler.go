package compiler

import (
	"bytes"
	"fmt"
	"io"

	"github.com/chai2010/ugo/ast"
	"github.com/chai2010/ugo/builtin"
	"github.com/chai2010/ugo/logger"
	"github.com/chai2010/ugo/token"
)

type Compiler struct {
	file   *ast.File
	scope  *Scope
	nextId int
}

func (p *Compiler) Compile(file *ast.File) string {
	var buf bytes.Buffer

	p.file = file
	p.scope = NewScope(Universe)

	p.genHeader(&buf, file)
	p.compileFile(&buf, file)
	p.genMain(&buf, file)

	return buf.String()
}

func (p *Compiler) enterScope() {
	p.scope = NewScope(p.scope)
}

func (p *Compiler) leaveScope() {
	p.scope = p.scope.Outer
}

func (p *Compiler) genHeader(w io.Writer, file *ast.File) {
	fmt.Fprintf(w, "; package %s\n", file.Pkg.Name)
	fmt.Fprintln(w, builtin.Header)
}

func (p *Compiler) genMain(w io.Writer, file *ast.File) {
	if file.Pkg.Name != "main" {
		return
	}
	for _, fn := range file.Funcs {
		if fn.Name == "main" {
			fmt.Fprintln(w, builtin.MainMain)
			return
		}
	}
}

func (p *Compiler) compileFile(w io.Writer, file *ast.File) {
	for _, g := range file.Globals {
		var llName = fmt.Sprintf("@ugo_%s_%s", file.Pkg.Name, g.Name.Name)
		p.scope.Insert(&Object{
			Name:   g.Name.Name,
			LLName: llName,
			Node:   g,
		})
		fmt.Fprintf(w, "%s = global i32 0\n", llName)
	}
	if len(file.Globals) != 0 {
		fmt.Fprintln(w)
	}
	for _, fn := range file.Funcs {
		p.compileFunc(w, file, fn)
	}
}

func (p *Compiler) compileFunc(w io.Writer, file *ast.File, fn *ast.Func) {
	if fn.Body == nil {
		fmt.Fprintf(w, "declare i32 @ugo_%s_%s() {\n", file.Pkg.Name, fn.Name)
		return
	}

	p.enterScope()
	defer p.leaveScope()

	fmt.Fprintf(w, "define i32 @ugo_%s_%s() {\n", file.Pkg.Name, fn.Name)
	p.compileStmt(w, fn.Body)

	fmt.Fprintln(w, "\tret i32 0")
	fmt.Fprintln(w, "}")
}

func (p *Compiler) compileStmt(w io.Writer, stmt ast.Stmt) {
	switch stmt := stmt.(type) {
	case *ast.VarSpec:
		var localName = "0"
		if stmt.Value != nil {
			localName = p.compileExpr(w, stmt.Value)
		}

		var llName = fmt.Sprintf("%%local_%s.pos.%d", stmt.Name.Name, stmt.VarPos)
		p.scope.Insert(&Object{
			Name:   stmt.Name.Name,
			LLName: llName,
			Node:   stmt,
		})

		fmt.Fprintf(w, "\t%s = alloca i32, align 4\n", llName)
		fmt.Fprintf(
			w, "\tstore i32 %s, i32* %s\n",
			localName, llName,
		)

	case *ast.AssignStmt:
		var varName string
		if obj := p.scope.Lookup(stmt.Target.Name); obj != nil {
			varName = obj.LLName
		} else if _, obj := p.scope.LookupParent(stmt.Target.Name); obj != nil {
			varName = obj.LLName
		} else {
			logger.Panicf("var %s undefined", stmt.Target.Name)
		}

		localName := p.compileExpr(w, stmt.Value)
		fmt.Fprintf(
			w, "\tstore i32 %s, i32* %s\n",
			localName, varName,
		)

	case *ast.BlockStmt:
		p.enterScope()
		defer p.leaveScope()

		for _, x := range stmt.List {
			p.compileStmt(w, x)
		}
	case *ast.ExprStmt:
		p.compileExpr(w, stmt.X)

	default:
		logger.Panicf("unknown: %[1]T, %[1]v", stmt)
	}
}

func (p *Compiler) compileExpr(w io.Writer, expr ast.Expr) (localName string) {
	switch expr := expr.(type) {
	case *ast.Ident:
		var varName string
		if obj := p.scope.Lookup(expr.Name); obj != nil {
			varName = obj.LLName
		} else if _, obj := p.scope.LookupParent(expr.Name); obj != nil {
			varName = obj.LLName
		} else {
			logger.Panicf("var %s undefined", expr.Name)
		}

		localName = p.genId()
		fmt.Fprintf(w, "\t%s = load i32, i32* %s, align 4\n",
			localName, varName,
		)
		return localName
	case *ast.Number:
		localName = p.genId()
		fmt.Fprintf(w, "\t%s = %s i32 %v, %v\n",
			localName, "add", `0`, expr.Value,
		)
		return localName
	case *ast.BinaryExpr:
		localName = p.genId()
		switch expr.Op {
		case token.ADD:
			fmt.Fprintf(w, "\t%s = %s i32 %v, %v\n",
				localName, "add", p.compileExpr(w, expr.X), p.compileExpr(w, expr.Y),
			)
			return localName
		case token.SUB:
			fmt.Fprintf(w, "\t%s = %s i32 %v, %v\n",
				localName, "sub", p.compileExpr(w, expr.X), p.compileExpr(w, expr.Y),
			)
			return localName
		case token.MUL:
			fmt.Fprintf(w, "\t%s = %s i32 %v, %v\n",
				localName, "mul", p.compileExpr(w, expr.X), p.compileExpr(w, expr.Y),
			)
			return localName
		case token.DIV:
			fmt.Fprintf(w, "\t%s = %s i32 %v, %v\n",
				localName, "div", p.compileExpr(w, expr.X), p.compileExpr(w, expr.Y),
			)
			return localName
		}
	case *ast.UnaryExpr:
		if expr.Op == token.SUB {
			localName = p.genId()
			fmt.Fprintf(w, "\t%s = %s i32 %v, %v\n",
				localName, "sub", `0`, p.compileExpr(w, expr.X),
			)
			return localName
		}
		return p.compileExpr(w, expr.X)
	case *ast.ParenExpr:
		return p.compileExpr(w, expr.X)
	case *ast.CallExpr:
		// call i32(i32) @ugo_builtin_exit(i32 %t2)
		localName = p.genId()
		fmt.Fprintf(w, "\t%s = call i32(i32) @ugo_builtin_%s(i32 %v)\n",
			localName, expr.FuncName.Name, p.compileExpr(w, expr.Args[0]),
		)
		return localName
	default:
		logger.Panicf("unknown: %[1]T, %[1]v", expr)
	}

	panic("unreachable")
}

func (p *Compiler) genId() string {
	id := fmt.Sprintf("%%t%d", p.nextId)
	p.nextId++
	return id
}
