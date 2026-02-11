package tsparse

import (
	"os"
	"path/filepath"
	"strings"

	"goodchanges/internal/diff"

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

	scriptKind := inferScriptKind(filePath)
	absPath := filePath
	if !filepath.IsAbs(filePath) {
		absPath, _ = filepath.Abs(filePath)
	}

	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{
		FileName:         absPath,
		JSDocParsingMode: ast.JSDocParsingModeParseNone,
	}, string(content), scriptKind)

	analysis := &FileAnalysis{
		Path:       filePath,
		SourceFile: sf,
	}

	lineMap := sf.ECMALineMap()

	for _, stmt := range sf.Statements.Nodes {
		extractImports(stmt, analysis)
		extractExports(stmt, sf, lineMap, analysis)
		extractDeclarations(stmt, sf, lineMap, analysis)
	}

	return analysis, nil
}

// FindAffectedSymbols returns the names of symbols affected by the given changed line ranges.
// Type-only changes do not propagate to runtime symbols unless includeTypes is true.
func FindAffectedSymbols(analysis *FileAnalysis, changedLines []diff.LineRange, includeTypes bool) []string {
	if analysis.SourceFile == nil {
		return nil
	}

	symByName := make(map[string]*SymbolDecl)
	for i := range analysis.Symbols {
		symByName[analysis.Symbols[i].Name] = &analysis.Symbols[i]
	}

	// Step 1: find directly changed symbols
	directlyChanged := make(map[string]bool)
	directlyChangedTypeOnly := make(map[string]bool)
	for _, sym := range analysis.Symbols {
		for _, lr := range changedLines {
			if sym.StartLine <= lr.End && sym.EndLine >= lr.Start {
				directlyChanged[sym.Name] = true
				directlyChangedTypeOnly[sym.Name] = sym.IsTypeOnly
				break
			}
		}
	}

	if len(directlyChanged) == 0 {
		var all []string
		for _, sym := range analysis.Symbols {
			if sym.IsExported && (!sym.IsTypeOnly || includeTypes) {
				all = append(all, sym.Name)
			}
		}
		for _, exp := range analysis.Exports {
			if exp.Source == "" && (!exp.IsTypeOnly || includeTypes) {
				all = append(all, exp.LocalName)
			}
		}
		return all
	}

	// Step 2: build intra-file reference graph
	sourceText := analysis.SourceFile.Text()
	dependsOn := make(map[string]map[string]bool)
	for _, sym := range analysis.Symbols {
		bodyText := ExtractTextForLines(sourceText, analysis.SourceFile.ECMALineMap(), sym.StartLine, sym.EndLine)
		deps := make(map[string]bool)
		for _, other := range analysis.Symbols {
			if other.Name != sym.Name && strings.Contains(bodyText, other.Name) {
				deps[other.Name] = true
			}
		}
		dependsOn[sym.Name] = deps
	}

	// Step 3: propagate — type-only changes don't propagate to runtime symbols
	affected := make(map[string]bool)
	affectedTypeOnly := make(map[string]bool)

	for name := range directlyChanged {
		affected[name] = true
		affectedTypeOnly[name] = directlyChangedTypeOnly[name]
	}

	changed := true
	for changed {
		changed = false
		for _, sym := range analysis.Symbols {
			if affected[sym.Name] {
				continue
			}
			for dep := range dependsOn[sym.Name] {
				if !affected[dep] {
					continue
				}
				if affectedTypeOnly[dep] && !sym.IsTypeOnly {
					continue
				}
				affected[sym.Name] = true
				affectedTypeOnly[sym.Name] = sym.IsTypeOnly
				changed = true
				break
			}
		}
	}

	var result []string
	for name := range affected {
		if affectedTypeOnly[name] && !includeTypes {
			continue
		}
		result = append(result, name)
	}
	return result
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

func extractExports(stmt *ast.Node, sf *ast.SourceFile, lineMap []core.TextPos, analysis *FileAnalysis) {
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

func extractDeclarations(stmt *ast.Node, sf *ast.SourceFile, lineMap []core.TextPos, analysis *FileAnalysis) {
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
