package tsparse

import (
	"os"
	"path/filepath"
	"strings"

	"goodchanges/tsgo-vendor/pkg/ast"
	"goodchanges/tsgo-vendor/pkg/core"
	"goodchanges/tsgo-vendor/pkg/parser"
)

type Import struct {
	Names  []string // imported names, or ["*:alias"] for namespace import
	Source string   // module specifier (e.g., "./Button/Button.js")
}

type Export struct {
	Name       string // exported name (or "default")
	LocalName  string // local name if aliased, otherwise same as Name
	Source     string // re-export source (empty if local export)
	IsTypeOnly bool
	IsStar     bool // export * from "..."
}

type SymbolDecl struct {
	Name       string
	Kind       string // "function", "class", "interface", "type", "variable", "enum"
	StartLine  int    // 1-based
	EndLine    int    // 1-based
	IsExported bool
	ExportName string
	IsTypeOnly bool // true for interface/type declarations
}

type FileAnalysis struct {
	Path       string
	Imports    []Import
	Exports    []Export
	Symbols    []SymbolDecl
	SourceFile *ast.SourceFile
}

func ParseFile(filePath string) (*FileAnalysis, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return ParseContent(string(content), filePath)
}

// ParseContent parses TypeScript/JavaScript source code from a string.
// The filename is used to infer the script kind (TS, TSX, JS, JSX).
func ParseContent(content string, filename string) (*FileAnalysis, error) {
	scriptKind := inferScriptKind(filename)
	absPath := filename
	if !filepath.IsAbs(filename) {
		absPath, _ = filepath.Abs(filename)
	}

	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{
		FileName:         absPath,
		JSDocParsingMode: ast.JSDocParsingModeParseNone,
	}, content, scriptKind)

	analysis := &FileAnalysis{
		Path:       filename,
		SourceFile: sf,
	}

	lineMap := sf.ECMALineMap()

	for _, stmt := range sf.Statements.Nodes {
		extractImports(stmt, analysis)
		extractExports(stmt, analysis)
		extractDeclarations(stmt, lineMap, analysis)
	}

	// Walk entire AST for dynamic imports: import("specifier")
	extractDynamicImports(sf, analysis)

	return analysis, nil
}

// ExtractTextForLines returns the text between the given 1-based line numbers.
func ExtractTextForLines(text string, lineMap []core.TextPos, startLine, endLine int) string {
	startIdx := 0
	endIdx := len(text)
	if startLine-1 < len(lineMap) {
		startIdx = int(lineMap[startLine-1])
	}
	if endLine < len(lineMap) {
		endIdx = int(lineMap[endLine])
	}
	if startIdx > len(text) {
		startIdx = len(text)
	}
	if endIdx > len(text) {
		endIdx = len(text)
	}
	return text[startIdx:endIdx]
}

func inferScriptKind(path string) core.ScriptKind {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".tsx":
		return core.ScriptKindTSX
	case ".ts":
		return core.ScriptKindTS
	case ".jsx":
		return core.ScriptKindJSX
	case ".js", ".mjs", ".cjs":
		return core.ScriptKindJS
	default:
		return core.ScriptKindTS
	}
}

func posToLine(pos int, lineMap []core.TextPos) int {
	line, _ := core.PositionToLineAndCharacter(pos, lineMap)
	return line + 1
}

func extractImports(stmt *ast.Node, analysis *FileAnalysis) {
	if !ast.IsImportDeclaration(stmt) {
		return
	}
	imp := stmt.AsImportDeclaration()
	source := strings.Trim(imp.ModuleSpecifier.Text(), "\"'`")

	var names []string
	if imp.ImportClause != nil {
		clause := imp.ImportClause.AsImportClause()
		if clause.Name() != nil {
			names = append(names, clause.Name().Text())
		}
		if clause.NamedBindings != nil {
			if ast.IsNamespaceImport(clause.NamedBindings) {
				ns := clause.NamedBindings.AsNamespaceImport()
				names = append(names, "*:"+ns.Name().Text())
			} else if ast.IsNamedImports(clause.NamedBindings) {
				ni := clause.NamedBindings.AsNamedImports()
				if ni.Elements != nil {
					for _, spec := range ni.Elements.Nodes {
						is := spec.AsImportSpecifier()
						names = append(names, is.Name().Text())
					}
				}
			}
		}
	}

	analysis.Imports = append(analysis.Imports, Import{
		Names:  names,
		Source: source,
	})
}

func extractExports(stmt *ast.Node, analysis *FileAnalysis) {
	switch {
	case ast.IsExportDeclaration(stmt):
		ed := stmt.AsExportDeclaration()
		source := ""
		if ed.ModuleSpecifier != nil {
			source = strings.Trim(ed.ModuleSpecifier.Text(), "\"'`")
		}

		if ed.ExportClause == nil {
			analysis.Exports = append(analysis.Exports, Export{
				Name:       "*",
				LocalName:  "*",
				Source:     source,
				IsTypeOnly: ed.IsTypeOnly,
				IsStar:     true,
			})
		} else if ast.IsNamedExports(ed.ExportClause) {
			ne := ed.ExportClause.AsNamedExports()
			if ne.Elements != nil {
				for _, spec := range ne.Elements.Nodes {
					es := spec.AsExportSpecifier()
					exportedName := es.Name().Text()
					localName := exportedName
					if es.PropertyName != nil {
						localName = es.PropertyName.Text()
					}
					analysis.Exports = append(analysis.Exports, Export{
						Name:       exportedName,
						LocalName:  localName,
						Source:     source,
						IsTypeOnly: ed.IsTypeOnly || es.IsTypeOnly,
					})
				}
			}
		} else if ast.IsNamespaceExport(ed.ExportClause) {
			ns := ed.ExportClause.AsNamespaceExport()
			analysis.Exports = append(analysis.Exports, Export{
				Name:       ns.Name().Text(),
				LocalName:  "*",
				Source:     source,
				IsTypeOnly: ed.IsTypeOnly,
			})
		}

	case ast.IsExportAssignment(stmt):
		analysis.Exports = append(analysis.Exports, Export{
			Name:      "default",
			LocalName: "default",
		})

	default:
		if ast.HasSyntacticModifier(stmt, ast.ModifierFlagsExport) {
			name := getDeclName(stmt)
			if name != "" {
				isDefault := ast.HasSyntacticModifier(stmt, ast.ModifierFlagsDefault)
				exportName := name
				if isDefault {
					exportName = "default"
				}
				analysis.Exports = append(analysis.Exports, Export{
					Name:      exportName,
					LocalName: name,
				})
			}
		}
	}
}

func extractDeclarations(stmt *ast.Node, lineMap []core.TextPos, analysis *FileAnalysis) {
	isExported := ast.HasSyntacticModifier(stmt, ast.ModifierFlagsExport)
	isDefault := ast.HasSyntacticModifier(stmt, ast.ModifierFlagsDefault)

	switch stmt.Kind {
	case ast.KindFunctionDeclaration:
		name := getDeclName(stmt)
		if name == "" && isDefault {
			name = "default"
		}
		if name != "" {
			exportName := name
			if isDefault {
				exportName = "default"
			}
			analysis.Symbols = append(analysis.Symbols, SymbolDecl{
				Name:       name,
				Kind:       "function",
				StartLine:  posToLine(stmt.Pos(), lineMap),
				EndLine:    posToLine(stmt.End(), lineMap),
				IsExported: isExported,
				ExportName: exportName,
			})
		}
	case ast.KindClassDeclaration:
		name := getDeclName(stmt)
		if name == "" && isDefault {
			name = "default"
		}
		if name != "" {
			exportName := name
			if isDefault {
				exportName = "default"
			}
			analysis.Symbols = append(analysis.Symbols, SymbolDecl{
				Name:       name,
				Kind:       "class",
				StartLine:  posToLine(stmt.Pos(), lineMap),
				EndLine:    posToLine(stmt.End(), lineMap),
				IsExported: isExported,
				ExportName: exportName,
			})
		}
	case ast.KindInterfaceDeclaration:
		name := getDeclName(stmt)
		if name != "" {
			analysis.Symbols = append(analysis.Symbols, SymbolDecl{
				Name:       name,
				Kind:       "interface",
				StartLine:  posToLine(stmt.Pos(), lineMap),
				EndLine:    posToLine(stmt.End(), lineMap),
				IsExported: isExported,
				ExportName: name,
				IsTypeOnly: true,
			})
		}
	case ast.KindTypeAliasDeclaration:
		name := getDeclName(stmt)
		if name != "" {
			analysis.Symbols = append(analysis.Symbols, SymbolDecl{
				Name:       name,
				Kind:       "type",
				StartLine:  posToLine(stmt.Pos(), lineMap),
				EndLine:    posToLine(stmt.End(), lineMap),
				IsExported: isExported,
				ExportName: name,
				IsTypeOnly: true,
			})
		}
	case ast.KindEnumDeclaration:
		name := getDeclName(stmt)
		if name != "" {
			analysis.Symbols = append(analysis.Symbols, SymbolDecl{
				Name:       name,
				Kind:       "enum",
				StartLine:  posToLine(stmt.Pos(), lineMap),
				EndLine:    posToLine(stmt.End(), lineMap),
				IsExported: isExported,
				ExportName: name,
			})
		}
	case ast.KindVariableStatement:
		vs := stmt.AsVariableStatement()
		if vs.DeclarationList != nil {
			dl := vs.DeclarationList.AsVariableDeclarationList()
			if dl.Declarations != nil {
				for _, decl := range dl.Declarations.Nodes {
					name := getDeclName(decl)
					if name != "" {
						analysis.Symbols = append(analysis.Symbols, SymbolDecl{
							Name:       name,
							Kind:       "variable",
							StartLine:  posToLine(stmt.Pos(), lineMap),
							EndLine:    posToLine(stmt.End(), lineMap),
							IsExported: isExported,
							ExportName: name,
						})
					}
				}
			}
		}
	}
}

func getDeclName(node *ast.Node) string {
	name := node.Name()
	if name == nil {
		return ""
	}
	if ast.IsIdentifier(name) {
		return name.Text()
	}
	return ""
}

// extractDynamicImports walks the full AST to find dynamic import() calls
// and adds them to the imports list.
//
// Pattern 1 (variable + property access): const mod = await import("pkg"); mod.Foo
//
//	→ Import{Names: ["Foo"], Source: "pkg"}
//
// Pattern 2 (destructured): const { Foo, Bar } = await import("pkg")
//
//	→ Import{Names: ["Foo", "Bar"], Source: "pkg"}
//
// Pattern 3 (.then callback): import("pkg").then((m) => m.Foo) or
//
//	import("pkg").then((m) => ({ default: m.Foo }))
//	→ Import{Names: ["Foo"], Source: "pkg"}
func extractDynamicImports(sf *ast.SourceFile, analysis *FileAnalysis) {
	// Pattern 3: import("pkg").then((m) => m.Foo)
	// Walk the full AST looking for .then() calls on import() expressions
	extractThenDynamicImports(sf, analysis)

	// Phase 1: collect dynamic imports assigned to variables or destructured
	// varName → specifier (for pattern 1)
	varImports := make(map[string]string)

	var walkPhase1 func(n *ast.Node)
	walkPhase1 = func(n *ast.Node) {
		if n == nil {
			return
		}

		if n.Kind == ast.KindVariableDeclaration {
			vd := n.AsVariableDeclaration()
			name := vd.Name()
			if name != nil && vd.Initializer != nil {
				specifier := extractDynamicImportSpecifier(vd.Initializer)
				if specifier != "" {
					if ast.IsObjectBindingPattern(name) {
						// Pattern 2: destructured
						bp := name.AsBindingPattern()
						if bp.Elements != nil {
							var names []string
							for _, elem := range bp.Elements.Nodes {
								be := elem.AsBindingElement()
								elemName := be.Name()
								if elemName != nil && ast.IsIdentifier(elemName) {
									names = append(names, elemName.Text())
								}
							}
							if len(names) > 0 {
								analysis.Imports = append(analysis.Imports, Import{
									Names:  names,
									Source: specifier,
								})
							}
						}
					} else if ast.IsIdentifier(name) {
						// Pattern 1: assigned to variable — collect for phase 2
						varImports[name.Text()] = specifier
					}
				}
			}
		}

		n.ForEachChild(func(child *ast.Node) bool {
			walkPhase1(child)
			return false
		})
	}

	for _, stmt := range sf.Statements.Nodes {
		walkPhase1(stmt)
	}

	if len(varImports) == 0 {
		return
	}

	// Phase 2: find property accesses on the collected variable names (e.g. mod.Foo)
	// Collect used property names per specifier
	propNames := make(map[string]map[string]bool) // specifier → set of property names

	var walkPhase2 func(n *ast.Node)
	walkPhase2 = func(n *ast.Node) {
		if n == nil {
			return
		}

		if n.Kind == ast.KindPropertyAccessExpression {
			pa := n.AsPropertyAccessExpression()
			if pa.Expression != nil && pa.Expression.Kind == ast.KindIdentifier {
				varName := pa.Expression.Text()
				if specifier, ok := varImports[varName]; ok {
					propName := pa.Name()
					if propName != nil {
						if propNames[specifier] == nil {
							propNames[specifier] = make(map[string]bool)
						}
						propNames[specifier][propName.Text()] = true
					}
				}
			}
		}

		n.ForEachChild(func(child *ast.Node) bool {
			walkPhase2(child)
			return false
		})
	}

	for _, stmt := range sf.Statements.Nodes {
		walkPhase2(stmt)
	}

	// Add imports for pattern 1 results
	for specifier, names := range propNames {
		var nameList []string
		for n := range names {
			nameList = append(nameList, n)
		}
		analysis.Imports = append(analysis.Imports, Import{
			Names:  nameList,
			Source: specifier,
		})
	}
}

// extractDynamicImportSpecifier checks if an expression is (or contains)
// a dynamic import() call and returns the module specifier string.
// Handles: import("pkg"), await import("pkg")
func extractDynamicImportSpecifier(expr *ast.Node) string {
	if expr == nil {
		return ""
	}
	// Unwrap await
	if expr.Kind == ast.KindAwaitExpression {
		expr = expr.AsAwaitExpression().Expression
	}
	if expr == nil {
		return ""
	}
	// Check for import() call
	if expr.Kind == ast.KindCallExpression {
		ce := expr.AsCallExpression()
		if ce.Expression != nil && ce.Expression.Kind == ast.KindImportKeyword {
			if ce.Arguments != nil && len(ce.Arguments.Nodes) > 0 {
				arg := ce.Arguments.Nodes[0]
				if arg.Kind == ast.KindStringLiteral {
					return strings.Trim(arg.Text(), "\"'`")
				}
			}
		}
	}
	return ""
}

// extractThenDynamicImports handles pattern 3: import("pkg").then((m) => m.Foo)
// Finds .then() calls on import() expressions, extracts the callback parameter name,
// and collects property accesses on that parameter as import names.
func extractThenDynamicImports(sf *ast.SourceFile, analysis *FileAnalysis) {
	staticSources := make(map[string]bool)
	for _, imp := range analysis.Imports {
		staticSources[imp.Source] = true
	}

	var walk func(n *ast.Node)
	walk = func(n *ast.Node) {
		if n == nil {
			return
		}

		// Look for: someExpr.then(callback) where someExpr is import("pkg")
		if n.Kind == ast.KindCallExpression {
			ce := n.AsCallExpression()
			if ce.Expression != nil && ce.Expression.Kind == ast.KindPropertyAccessExpression {
				pa := ce.Expression.AsPropertyAccessExpression()
				propName := pa.Name()
				if propName != nil && propName.Text() == "then" {
					// Check if the object is an import() call
					specifier := extractDynamicImportSpecifier(pa.Expression)
					if specifier != "" && !staticSources[specifier] {
						// Extract property accesses from the .then callback
						if ce.Arguments != nil && len(ce.Arguments.Nodes) > 0 {
							names := extractNamesFromThenCallback(ce.Arguments.Nodes[0])
							if len(names) > 0 {
								analysis.Imports = append(analysis.Imports, Import{
									Names:  names,
									Source: specifier,
								})
								staticSources[specifier] = true
							}
						}
					}
				}
			}
		}

		n.ForEachChild(func(child *ast.Node) bool {
			walk(child)
			return false
		})
	}

	for _, stmt := range sf.Statements.Nodes {
		walk(stmt)
	}
}

// extractNamesFromThenCallback extracts property names accessed on the callback
// parameter. Handles arrow functions and function expressions.
// e.g. (m) => m.Foo → ["Foo"]
// e.g. (m) => ({ default: m.Foo }) → ["Foo"]
// e.g. ({ Foo }) => ... → ["Foo"]
func extractNamesFromThenCallback(callbackNode *ast.Node) []string {
	if callbackNode == nil {
		return nil
	}

	var paramName string
	var body *ast.Node

	switch callbackNode.Kind {
	case ast.KindArrowFunction:
		af := callbackNode.AsArrowFunction()
		if af.Parameters != nil && len(af.Parameters.Nodes) > 0 {
			param := af.Parameters.Nodes[0]
			pName := param.Name()
			if pName != nil {
				if ast.IsIdentifier(pName) {
					paramName = pName.Text()
				} else if ast.IsObjectBindingPattern(pName) {
					// Destructured parameter: ({ Foo, Bar }) => ...
					bp := pName.AsBindingPattern()
					if bp.Elements != nil {
						var names []string
						for _, elem := range bp.Elements.Nodes {
							be := elem.AsBindingElement()
							elemName := be.Name()
							if elemName != nil && ast.IsIdentifier(elemName) {
								names = append(names, elemName.Text())
							}
						}
						return names
					}
					return nil
				}
			}
		}
		body = af.Body
	case ast.KindFunctionExpression:
		fe := callbackNode.AsFunctionExpression()
		if fe.Parameters != nil && len(fe.Parameters.Nodes) > 0 {
			param := fe.Parameters.Nodes[0]
			pName := param.Name()
			if pName != nil && ast.IsIdentifier(pName) {
				paramName = pName.Text()
			}
		}
		body = fe.Body
	default:
		return nil
	}

	if paramName == "" || body == nil {
		return nil
	}

	// Walk the body for paramName.Property accesses
	nameSet := make(map[string]bool)
	var walkBody func(n *ast.Node)
	walkBody = func(n *ast.Node) {
		if n == nil {
			return
		}
		if n.Kind == ast.KindPropertyAccessExpression {
			pa := n.AsPropertyAccessExpression()
			if pa.Expression != nil && pa.Expression.Kind == ast.KindIdentifier {
				if pa.Expression.Text() == paramName {
					prop := pa.Name()
					if prop != nil {
						nameSet[prop.Text()] = true
					}
				}
			}
		}
		n.ForEachChild(func(child *ast.Node) bool {
			walkBody(child)
			return false
		})
	}
	walkBody(body)

	var names []string
	for n := range nameSet {
		names = append(names, n)
	}
	return names
}
