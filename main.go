package main

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"os"
	"os/signal"

	"golang.org/x/tools/go/packages"
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := Main(ctx, flag.Args())
	if err != nil {
		log.Fatal(err)
	}
}

func Main(ctx context.Context, pattern []string) error {
	ps, err := Packages(ctx, pattern)
	if err != nil {
		return err
	}

	var out []string
	totalImplicit, totalExplicit := 0, 0
	for _, pkg := range ps {
		implicit, explicit := Find(pkg)
		if implicit+explicit > 0 {
			totalImplicit += implicit
			totalExplicit += explicit
			out = append(out, fmt.Sprintf("%s: %d implicit, %d explicit; all %d", pkg.ID, implicit, explicit, implicit+explicit))
		}
	}

	for _, line := range out {
		fmt.Println(line)
	}
	if len(ps) > 1 {
		fmt.Printf("\nTOTAL: %d implicit, %d explicit; all %d\n", totalImplicit, totalExplicit, totalImplicit+totalExplicit)
	}
	return nil
}

func Packages(ctx context.Context, pattern []string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Context: ctx,

		Mode: packages.NeedTypesInfo | packages.NeedTypes | packages.NeedSyntax | packages.NeedFiles,
	}
	ps, err := packages.Load(cfg, pattern...)
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(ps) > 0 {
		return nil, fmt.Errorf("could not load packages")
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("no packages to load")
	}
	return ps, nil
}

type counter struct {
	pkg                *packages.Package
	implicit, explicit int
}

func newCounter(pkg *packages.Package) *counter {
	return &counter{pkg: pkg}
}

func (c *counter) inspect(n ast.Node) bool {
	hit := false
	switch n := n.(type) {
	case *ast.IfStmt:
		// if-else statement whose branches only set a number
		if PotentialIversonIf(c.pkg, n) {
			c.implicit++
			hit = true
		} else {
			// we need to manually scan the blocks and expressions to avoid false positives in else-if's
			c.recurOnIf(n)
			return false
		}

	case *ast.CallExpr:
		// calling a func(~number) ~bool
		_, ok := n.Fun.(*ast.SelectorExpr)
		if !ok && IsBracketFunc(c.pkg.TypesInfo.TypeOf(n.Fun)) {
			c.explicit++
			hit = true
		}

	case *ast.IndexExpr:
		// reading from a map[~bool]~number
		if IsMapBracket(c.pkg.TypesInfo.TypeOf(n.X)) {
			c.explicit++
			hit = true
		}
	}
	if hit {
		log.Print(c.pkg.Fset.Position(n.Pos()))
	}
	return true
}

func (c *counter) recurOnIf(n *ast.IfStmt) {
	if n.Init != nil {
		ast.Inspect(n.Init, c.inspect)
	}
	ast.Inspect(n.Cond, c.inspect)
	ast.Inspect(n.Body, c.inspect)
	switch Else := n.Else.(type) {
	case nil:
	case *ast.IfStmt:
		c.recurOnIf(Else)
	case *ast.BlockStmt:
		ast.Inspect(Else, c.inspect)
	}
}

func Find(pkg *packages.Package) (implicit, explicit int) {
	c := newCounter(pkg)
	for _, file := range pkg.Syntax {
		ast.Inspect(file, c.inspect)
	}
	return c.implicit, c.explicit
}

func PotentialIversonIf(pkg *packages.Package, cond *ast.IfStmt) bool {
	if cond.Else == nil {
		return false
	}
	elseBlock, ok := cond.Else.(*ast.BlockStmt)
	if !ok {
		return false
	}
	return BranchOnlySetsNumber(pkg, cond.Body) && BranchOnlySetsNumber(pkg, elseBlock)
}

// BranchOnlySetsNumber true for an if without an else whose body is just x = n for a ~number which is either a literal or ident
func BranchOnlySetsNumber(pkg *packages.Package, body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	stmt := body.List[0]
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return false
	}
	if assign.Tok != token.ASSIGN {
		return false
	}
	if len(assign.Lhs) != 1 {
		return false
	}
	x := assign.Rhs[0]
	switch x.(type) {
	case *ast.BasicLit, *ast.Ident:
	default:
		return false
	}
	return numeric(pkg.TypesInfo.TypeOf(x))
}

// IsBracketFunc returns true if the typ is a func from a ~bool to a ~number.
func IsBracketFunc(typ types.Type) bool {
	sig, ok := typ.Underlying().(*types.Signature)
	if !ok {
		return false
	}
	if sig.Recv() != nil {
		return false
	}
	in, out := sig.Params(), sig.Results()
	if in == nil || out == nil {
		return false
	}
	if in.Len() != 1 || out.Len() != 1 || sig.Variadic() {
		return false
	}
	return boolish(in.At(0).Type()) && numeric(out.At(0).Type())
}

func IsMapBracket(typ types.Type) bool {
	m, ok := typ.(*types.Map)
	if !ok {
		return false
	}
	return boolish(m.Key()) && numeric(m.Elem())
}

func boolish(typ types.Type) bool {
	if typ == nil {
		return false
	}
	t, ok := types.Default(typ).Underlying().(*types.Basic)
	if !ok {
		return false
	}
	return t.Kind() == types.Bool
}

func numeric(typ types.Type) bool {
	if typ == nil {
		return false
	}
	t, ok := types.Default(typ).Underlying().(*types.Basic)
	if !ok {
		return false
	}
	switch t.Kind() {
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64, types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Float32, types.Float64, types.Complex64, types.Complex128:
		return true
	}
	return false

}
