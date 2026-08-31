// Command unusedglobals reports package-level constants and variables with no
// references from production Go files, plus struct fields that production code
// writes but never reads. Unlike ordinary unused-code checks, it also considers
// exported objects because Sermo's scanned packages are internal to this
// repository.
package main

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

const (
	analysisTimeout = 2 * time.Minute
	exitOK          = 0
	exitFindings    = 1
	exitUsage       = 2
)

type objectKind string

const (
	constantKind objectKind = "const"
	fieldKind    objectKind = "field"
	variableKind objectKind = "var"
)

type objectKey struct {
	packagePath string
	name        string
	kind        objectKind
}

type finding struct {
	position token.Position
	key      objectKey
}

type fieldKey struct {
	packagePath string
	filename    string
	name        string
	line        int
	column      int
}

type fieldState struct {
	finding finding
	read    bool
	written bool
}

func main() {
	//nolint:forbidigo // A standalone analyzer must return its diagnostic status to make/CI.
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("unusedglobals", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", ".", "module directory used to resolve package patterns")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	patterns := flags.Args()
	if len(patterns) == 0 {
		fmt.Fprintln(stderr, "unusedglobals: at least one package pattern is required")
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), analysisTimeout)
	defer cancel()
	findings, err := findUnused(ctx, *dir, patterns)
	if err != nil {
		fmt.Fprintf(stderr, "unusedglobals: %v\n", err)
		return exitUsage
	}
	for _, item := range findings {
		fmt.Fprintln(stdout, item.message(*dir))
	}
	if len(findings) > 0 {
		return exitFindings
	}
	return exitOK
}

func findUnused(ctx context.Context, dir string, patterns []string) ([]finding, error) {
	pkgs, err := packages.Load(&packages.Config{
		Context: ctx,
		Dir:     dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedImports | packages.NeedDeps,
		Tests: false,
	}, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load production packages: %w", err)
	}
	if err := packageErrors(pkgs); err != nil {
		return nil, err
	}

	declared := make(map[objectKey]finding)
	for _, pkg := range pkgs {
		if pkg.Types == nil || pkg.Fset == nil {
			return nil, fmt.Errorf("load production package %q without type information", pkg.PkgPath)
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			key, ok := packageObjectKey(obj)
			if !ok {
				continue
			}
			declared[key] = finding{position: pkg.Fset.Position(obj.Pos()), key: key}
		}
	}

	used := make(map[objectKey]bool)
	for _, pkg := range pkgs {
		for _, obj := range pkg.TypesInfo.Uses {
			key, ok := packageObjectKey(obj)
			if ok {
				used[key] = true
			}
		}
	}

	findings := make([]finding, 0)
	for key, item := range declared {
		if !used[key] {
			findings = append(findings, item)
		}
	}

	fields := collectFields(pkgs)
	for _, pkg := range pkgs {
		markFieldUsage(pkg, fields)
	}
	for _, state := range fields {
		if state.written && !state.read {
			findings = append(findings, state.finding)
		}
	}
	slices.SortFunc(findings, func(a, b finding) int {
		if byFile := strings.Compare(a.position.Filename, b.position.Filename); byFile != 0 {
			return byFile
		}
		if a.position.Line != b.position.Line {
			return a.position.Line - b.position.Line
		}
		return strings.Compare(a.key.name, b.key.name)
	})
	return findings, nil
}

func collectFields(pkgs []*packages.Package) map[fieldKey]*fieldState {
	fields := make(map[fieldKey]*fieldState)
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				structNode, ok := node.(*ast.StructType)
				if !ok {
					return true
				}
				structType := underlyingStruct(pkg.TypesInfo.TypeOf(structNode))
				if structType == nil {
					return true
				}
				for index := range structType.NumFields() {
					field := structType.Field(index)
					if field.Anonymous() || structType.Tag(index) != "" {
						continue
					}
					key, position, ok := fieldObjectKey(pkg.Fset, field)
					if !ok {
						continue
					}
					fields[key] = &fieldState{finding: finding{
						position: position,
						key: objectKey{
							packagePath: field.Pkg().Path(),
							name:        field.Name(),
							kind:        fieldKind,
						},
					}}
				}
				return true
			})
		}
	}
	return fields
}

func markFieldUsage(pkg *packages.Package, fields map[fieldKey]*fieldState) {
	parents := parentNodes(pkg.Syntax)
	for expression, selection := range pkg.TypesInfo.Selections {
		field, ok := selection.Obj().(*types.Var)
		if !ok || !field.IsField() {
			continue
		}
		read, written := selectorAccess(expression, parents)
		markField(pkg.Fset, fields, field, read, written)
	}

	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			markFieldNode(pkg, fields, node)
			return true
		})
	}
}

func markFieldNode(pkg *packages.Package, fields map[fieldKey]*fieldState, node ast.Node) {
	switch value := node.(type) {
	case *ast.AssignStmt:
		markInterfaceAssignments(pkg, fields, value.Lhs, value.Rhs)
	case *ast.BinaryExpr:
		markComparedFields(pkg, fields, value)
	case *ast.CompositeLit:
		markCompositeLiteralFields(pkg, fields, value)
	case *ast.CallExpr:
		markReflectiveFields(pkg, fields, value)
		markInterfaceCallArguments(pkg, fields, value)
	case *ast.IndexExpr:
		markMapKeyFields(pkg, fields, value)
	case *ast.SendStmt:
		markInterfaceSend(pkg, fields, value)
	case *ast.ValueSpec:
		markInterfaceValues(pkg, fields, value)
	}
}

func markComparedFields(pkg *packages.Package, fields map[fieldKey]*fieldState, expression *ast.BinaryExpr) {
	if expression.Op != token.EQL && expression.Op != token.NEQ {
		return
	}
	markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(expression.X), false, make(map[types.Type]bool))
	markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(expression.Y), false, make(map[types.Type]bool))
}

func markMapKeyFields(pkg *packages.Package, fields map[fieldKey]*fieldState, expression *ast.IndexExpr) {
	mapType, ok := pkg.TypesInfo.TypeOf(expression.X).Underlying().(*types.Map)
	if ok {
		markTypeFieldsRead(pkg.Fset, fields, mapType.Key(), false, make(map[types.Type]bool))
	}
}

func markInterfaceSend(pkg *packages.Package, fields map[fieldKey]*fieldState, send *ast.SendStmt) {
	channel, ok := pkg.TypesInfo.TypeOf(send.Chan).Underlying().(*types.Chan)
	if ok && isInterface(channel.Elem()) {
		markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(send.Value), false, make(map[types.Type]bool))
	}
}

func markInterfaceValues(pkg *packages.Package, fields map[fieldKey]*fieldState, spec *ast.ValueSpec) {
	if spec.Type == nil || !isInterface(pkg.TypesInfo.TypeOf(spec.Type)) {
		return
	}
	for _, expression := range spec.Values {
		markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(expression), false, make(map[types.Type]bool))
	}
}

func parentNodes(files []*ast.File) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	for _, file := range files {
		stack := make([]ast.Node, 0)
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if len(stack) > 0 {
				parents[node] = stack[len(stack)-1]
			}
			stack = append(stack, node)
			return true
		})
	}
	return parents
}

func selectorAccess(expression *ast.SelectorExpr, parents map[ast.Node]ast.Node) (read, written bool) {
	var target ast.Node = expression
	for {
		parent, ok := parents[target].(*ast.ParenExpr)
		if !ok || parent.X != target {
			break
		}
		target = parent
	}

	switch parent := parents[target].(type) {
	case *ast.AssignStmt:
		if parent == nil {
			return true, false
		}
		if expressionIndex(parent.Lhs, target) < 0 {
			return true, false
		}
		if parent.Tok == token.ASSIGN || parent.Tok == token.DEFINE {
			return false, true
		}
		return true, true
	case *ast.IncDecStmt:
		if parent == nil {
			return true, false
		}
		if parent.X == target {
			return true, true
		}
	case *ast.RangeStmt:
		if parent == nil {
			return true, false
		}
		if parent.Key == target || parent.Value == target {
			return false, true
		}
	}
	return true, false
}

func expressionIndex(expressions []ast.Expr, target ast.Node) int {
	for index, expression := range expressions {
		if expression == target {
			return index
		}
	}
	return -1
}

func markCompositeLiteralFields(pkg *packages.Package, fields map[fieldKey]*fieldState, literal *ast.CompositeLit) {
	markCompositeInterfaceEscapes(pkg, fields, literal)
	structType := underlyingStruct(pkg.TypesInfo.TypeOf(literal))
	if structType == nil {
		return
	}
	for index, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			identifier, ok := pair.Key.(*ast.Ident)
			if !ok {
				continue
			}
			field, ok := pkg.TypesInfo.Uses[identifier].(*types.Var)
			if ok && field.IsField() {
				markField(pkg.Fset, fields, field, false, true)
			}
			continue
		}
		if index < structType.NumFields() {
			markField(pkg.Fset, fields, structType.Field(index), false, true)
		}
	}
}

func markCompositeInterfaceEscapes(pkg *packages.Package, fields map[fieldKey]*fieldState, literal *ast.CompositeLit) {
	switch composite := pkg.TypesInfo.TypeOf(literal).Underlying().(type) {
	case *types.Map:
		markMapCompositeEscapes(pkg, fields, literal, composite)
	case *types.Slice:
		markSequenceCompositeEscapes(pkg, fields, literal, composite.Elem())
	case *types.Array:
		markSequenceCompositeEscapes(pkg, fields, literal, composite.Elem())
	case *types.Struct:
		markStructCompositeEscapes(pkg, fields, literal, composite)
	}
}

func markMapCompositeEscapes(pkg *packages.Package, fields map[fieldKey]*fieldState, literal *ast.CompositeLit, mapType *types.Map) {
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		markTypeFieldsRead(pkg.Fset, fields, mapType.Key(), false, make(map[types.Type]bool))
		if isInterface(mapType.Elem()) {
			markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(pair.Value), false, make(map[types.Type]bool))
		}
	}
}

func markSequenceCompositeEscapes(pkg *packages.Package, fields map[fieldKey]*fieldState, literal *ast.CompositeLit, elementType types.Type) {
	if !isInterface(elementType) {
		return
	}
	for _, element := range literal.Elts {
		markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(element), false, make(map[types.Type]bool))
	}
}

func markStructCompositeEscapes(pkg *packages.Package, fields map[fieldKey]*fieldState, literal *ast.CompositeLit, structType *types.Struct) {
	for index, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			markKeyedInterfaceField(pkg, fields, pair)
			continue
		}
		if index < structType.NumFields() && isInterface(structType.Field(index).Type()) {
			markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(element), false, make(map[types.Type]bool))
		}
	}
}

func markKeyedInterfaceField(pkg *packages.Package, fields map[fieldKey]*fieldState, pair *ast.KeyValueExpr) {
	identifier, ok := pair.Key.(*ast.Ident)
	if !ok {
		return
	}
	field, ok := pkg.TypesInfo.Uses[identifier].(*types.Var)
	if ok && isInterface(field.Type()) {
		markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(pair.Value), false, make(map[types.Type]bool))
	}
}

func markInterfaceAssignments(pkg *packages.Package, fields map[fieldKey]*fieldState, left, right []ast.Expr) {
	if len(left) != len(right) {
		return
	}
	for index := range left {
		if isInterface(pkg.TypesInfo.TypeOf(left[index])) {
			markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(right[index]), false, make(map[types.Type]bool))
		}
	}
}

func markInterfaceCallArguments(pkg *packages.Package, fields map[fieldKey]*fieldState, call *ast.CallExpr) {
	if typeAndValue, ok := pkg.TypesInfo.Types[call.Fun]; ok && typeAndValue.IsType() {
		if isInterface(typeAndValue.Type) && len(call.Args) == 1 {
			markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(call.Args[0]), false, make(map[types.Type]bool))
		}
	}
}

func markReflectiveFields(pkg *packages.Package, fields map[fieldKey]*fieldState, call *ast.CallExpr) {
	function := calledFunction(pkg.TypesInfo, call.Fun)
	if function == nil || function.Pkg() == nil {
		return
	}
	for _, index := range reflectiveArgumentIndexes(function.Pkg().Path(), function.Name()) {
		if index < len(call.Args) {
			markTypeFieldsRead(pkg.Fset, fields, pkg.TypesInfo.TypeOf(call.Args[index]), true, make(map[types.Type]bool))
		}
	}
}

func calledFunction(info *types.Info, expression ast.Expr) *types.Func {
	switch function := expression.(type) {
	case *ast.Ident:
		result, _ := info.Uses[function].(*types.Func)
		return result
	case *ast.SelectorExpr:
		if selection := info.Selections[function]; selection != nil {
			result, _ := selection.Obj().(*types.Func)
			return result
		}
		result, _ := info.Uses[function.Sel].(*types.Func)
		return result
	default:
		return nil
	}
}

func reflectiveArgumentIndexes(packagePath, name string) []int {
	switch packagePath {
	case "text/template", "html/template":
		switch name {
		case "Execute":
			return []int{1}
		case "ExecuteTemplate":
			return []int{2}
		}
	case "encoding/json", "encoding/xml", "encoding/gob", "github.com/goccy/go-yaml":
		switch name {
		case "Marshal", "MarshalIndent", "Encode":
			return []int{0}
		}
	case "reflect":
		if name == "ValueOf" {
			return []int{0}
		}
	}
	return nil
}

func markTypeFieldsRead(fset *token.FileSet, fields map[fieldKey]*fieldState, value types.Type, methods bool, visited map[types.Type]bool) {
	if value == nil || visited[value] {
		return
	}
	visited[value] = true
	switch typed := value.(type) {
	case *types.Pointer:
		markTypeFieldsRead(fset, fields, typed.Elem(), methods, visited)
	case *types.Slice:
		markTypeFieldsRead(fset, fields, typed.Elem(), methods, visited)
	case *types.Array:
		markTypeFieldsRead(fset, fields, typed.Elem(), methods, visited)
	case *types.Map:
		markTypeFieldsRead(fset, fields, typed.Elem(), methods, visited)
	case *types.Named:
		markTypeFieldsRead(fset, fields, typed.Underlying(), methods, visited)
		if methods {
			markMethodResultFieldsRead(fset, fields, typed, visited)
		}
	case *types.Struct:
		for field := range typed.Fields() {
			markField(fset, fields, field, true, false)
			markTypeFieldsRead(fset, fields, field.Type(), methods, visited)
		}
	}
}

func markMethodResultFieldsRead(fset *token.FileSet, fields map[fieldKey]*fieldState, named *types.Named, visited map[types.Type]bool) {
	for _, receiver := range []types.Type{named, types.NewPointer(named)} {
		methods := types.NewMethodSet(receiver)
		for method := range methods.Methods() {
			function, ok := method.Obj().(*types.Func)
			if !ok {
				continue
			}
			signature, _ := function.Type().(*types.Signature)
			if signature == nil {
				continue
			}
			results := signature.Results()
			for result := range results.Variables() {
				markTypeFieldsRead(fset, fields, result.Type(), true, visited)
			}
		}
	}
}

func isInterface(value types.Type) bool {
	if value == nil {
		return false
	}
	if _, parameter := value.(*types.TypeParam); parameter {
		return false
	}
	_, ok := value.Underlying().(*types.Interface)
	return ok
}

func markField(fset *token.FileSet, fields map[fieldKey]*fieldState, field *types.Var, read, written bool) {
	key, _, ok := fieldObjectKey(fset, field)
	if !ok {
		return
	}
	state := fields[key]
	if state == nil {
		return
	}
	state.read = state.read || read
	state.written = state.written || written
}

func fieldObjectKey(fset *token.FileSet, field *types.Var) (fieldKey, token.Position, bool) {
	if field == nil || !field.IsField() || field.Pkg() == nil || field.Name() == "_" {
		return fieldKey{}, token.Position{}, false
	}
	position := fset.Position(field.Pos())
	if !position.IsValid() {
		return fieldKey{}, token.Position{}, false
	}
	return fieldKey{
		packagePath: field.Pkg().Path(),
		filename:    position.Filename,
		name:        field.Name(),
		line:        position.Line,
		column:      position.Column,
	}, position, true
}

func underlyingStruct(value types.Type) *types.Struct {
	if value == nil {
		return nil
	}
	if pointer, ok := value.(*types.Pointer); ok {
		value = pointer.Elem()
	}
	result, _ := value.Underlying().(*types.Struct)
	return result
}

func packageObjectKey(obj types.Object) (objectKey, bool) {
	if obj == nil || obj.Pkg() == nil || obj.Name() == "_" || obj.Parent() != obj.Pkg().Scope() {
		return objectKey{}, false
	}
	key := objectKey{packagePath: obj.Pkg().Path(), name: obj.Name()}
	switch obj.(type) {
	case *types.Const:
		key.kind = constantKind
	case *types.Var:
		key.kind = variableKind
	default:
		return objectKey{}, false
	}
	return key, true
}

func packageErrors(pkgs []*packages.Package) error {
	var messages []string
	for _, pkg := range pkgs {
		for _, pkgErr := range pkg.Errors {
			messages = append(messages, pkgErr.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	slices.Sort(messages)
	return fmt.Errorf("type-check production packages:\n%s", strings.Join(messages, "\n"))
}

func (f finding) message(dir string) string {
	position := f.position
	if absoluteDir, err := filepath.Abs(dir); err == nil {
		if relative, err := filepath.Rel(absoluteDir, position.Filename); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			position.Filename = relative
		}
	}
	if f.key.kind == fieldKind {
		return fmt.Sprintf("%s: struct field %s is written but never read in production", position, f.key.name)
	}
	return fmt.Sprintf("%s: package-level %s %s has no production references", position, f.key.kind, f.key.name)
}
