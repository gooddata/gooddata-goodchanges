package analyzer

import (
	"strings"

	"goodchanges/internal/tsparse"
	"goodchanges/tsgo-vendor/pkg/ast"
)

// findAffectedSymbolsByASTDiff compares OLD and NEW file ASTs to find which symbols changed.
// Returns symbol names that have runtime changes (or type-only changes if includeTypes is true).
//
// For each symbol in the NEW file:
//   - If it didn't exist in the OLD file → new symbol, affected
//   - If it existed → compare body text. If different, check if change is type-only.
//
// Type-only classification:
//   - interface/type declarations → always type-only
//   - function/class/variable/enum → extract runtime-only text (strip type annotations,
//     as/satisfies expressions), compare. If runtime texts match → type-only change.
func findAffectedSymbolsByASTDiff(oldAnalysis *tsparse.FileAnalysis, newAnalysis *tsparse.FileAnalysis, oldContent string, includeTypes bool) []string {
	if newAnalysis == nil || newAnalysis.SourceFile == nil {
		return nil
	}

	newText := newAnalysis.SourceFile.Text()

	// Build map of old symbol name → body text
	oldSymbolTexts := make(map[string]string)
	oldSymbolRuntimeTexts := make(map[string]string)
	if oldAnalysis != nil && oldAnalysis.SourceFile != nil {
		oldText := oldAnalysis.SourceFile.Text()
		oldLineMap := oldAnalysis.SourceFile.ECMALineMap()
		for _, sym := range oldAnalysis.Symbols {
			body := tsparse.ExtractTextForLines(oldText, oldLineMap, sym.StartLine, sym.EndLine)
			oldSymbolTexts[sym.Name] = normalizeWhitespace(body)
		}
		// Also extract runtime-only texts for old symbols using AST
		oldStmtMap := buildStmtMap(oldAnalysis.SourceFile)
		for _, sym := range oldAnalysis.Symbols {
			if sym.IsTypeOnly {
				continue
			}
			if stmt, ok := oldStmtMap[sym.Name]; ok {
				oldSymbolRuntimeTexts[sym.Name] = extractRuntimeText(stmt, oldText)
			}
		}
	}

	// Compare each new symbol against old
	newLineMap := newAnalysis.SourceFile.ECMALineMap()
	newStmtMap := buildStmtMap(newAnalysis.SourceFile)

	var affected []string
	for _, sym := range newAnalysis.Symbols {
		newBody := tsparse.ExtractTextForLines(newText, newLineMap, sym.StartLine, sym.EndLine)
		newBodyNorm := normalizeWhitespace(newBody)

		oldBodyNorm, existedBefore := oldSymbolTexts[sym.Name]
		if !existedBefore {
			// New symbol — it's affected
			if sym.IsTypeOnly && !includeTypes {
				continue
			}
			debugf("    %s: NEW symbol", sym.Name)
			affected = append(affected, sym.Name)
			continue
		}

		if newBodyNorm == oldBodyNorm {
			// Identical — not affected
			continue
		}

		// Symbol text changed. Determine if it's type-only or runtime.
		if sym.IsTypeOnly {
			// interface/type alias — always type-only
			if includeTypes {
				debugf("    %s: type-only change (interface/type)", sym.Name)
				affected = append(affected, sym.Name)
			}
			continue
		}

		// Runtime symbol changed — check if the change is type-only
		// by comparing runtime-stripped texts
		oldRuntime := oldSymbolRuntimeTexts[sym.Name]
		newRuntime := ""
		if stmt, ok := newStmtMap[sym.Name]; ok {
			newRuntime = extractRuntimeText(stmt, newText)
		}

		if oldRuntime != "" && newRuntime != "" && oldRuntime == newRuntime {
			// Only type annotations changed (e.g. `x = foo` → `x = foo as Bar`)
			if includeTypes {
				debugf("    %s: type-only change (runtime text identical)", sym.Name)
				affected = append(affected, sym.Name)
			}
			continue
		}

		// Runtime change
		debugf("    %s: RUNTIME change", sym.Name)
		affected = append(affected, sym.Name)
	}

	// Intra-file propagation: if symbol A changed and symbol B references A,
	// then B is also affected. E.g. `UiPagedVirtualListNotWrapped` changed,
	// `UiPagedVirtualList = memo(UiPagedVirtualListNotWrapped)` is also affected.
	if len(affected) > 0 && newAnalysis.SourceFile != nil {
		affectedSet := make(map[string]bool)
		affectedTypeOnly := make(map[string]bool)
		for _, name := range affected {
			affectedSet[name] = true
			// Look up if this symbol is type-only
			for _, sym := range newAnalysis.Symbols {
				if sym.Name == name {
					affectedTypeOnly[name] = sym.IsTypeOnly
					break
				}
			}
		}

		// Build intra-file reference graph
		dependsOn := make(map[string]map[string]bool)
		for _, sym := range newAnalysis.Symbols {
			bodyText := tsparse.ExtractTextForLines(newText, newLineMap, sym.StartLine, sym.EndLine)
			deps := make(map[string]bool)
			for _, other := range newAnalysis.Symbols {
				if other.Name != sym.Name && strings.Contains(bodyText, other.Name) {
					deps[other.Name] = true
				}
			}
			dependsOn[sym.Name] = deps
		}

		// Propagate until stable
		changed := true
		for changed {
			changed = false
			for _, sym := range newAnalysis.Symbols {
				if affectedSet[sym.Name] {
					continue
				}
				for dep := range dependsOn[sym.Name] {
					if !affectedSet[dep] {
						continue
					}
					// Type-only changes don't propagate to runtime symbols
					if affectedTypeOnly[dep] && !sym.IsTypeOnly {
						continue
					}
					affectedSet[sym.Name] = true
					affectedTypeOnly[sym.Name] = sym.IsTypeOnly
					changed = true
					debugf("    %s: affected via intra-file dep on %s", sym.Name, dep)
					break
				}
			}
		}

		// Rebuild affected list with propagated symbols
		affected = nil
		for _, sym := range newAnalysis.Symbols {
			if !affectedSet[sym.Name] {
				continue
			}
			if affectedTypeOnly[sym.Name] && !includeTypes {
				continue
			}
			affected = append(affected, sym.Name)
		}
	}

	// Fallback: if no symbols were detected but the file clearly changed,
	// check if changes are outside any symbol (e.g. top-level side effects,
	// copyright comments). If there are exported symbols, taint them all.
	if len(affected) == 0 && oldAnalysis != nil {
		oldText := ""
		if oldAnalysis.SourceFile != nil {
			oldText = oldAnalysis.SourceFile.Text()
		}
		if normalizeWhitespace(oldText) != normalizeWhitespace(newText) {
			// File changed but no symbol was affected — changes are outside symbols.
			// This could be comments, imports, or top-level side-effect code.
			// Don't taint anything — changes outside symbols don't affect exports.
			debugf("    file changed but no symbols affected (comments/imports only)")
		}
	}

	return affected
}

// buildStmtMap maps symbol names to their AST statement nodes.
func buildStmtMap(sf *ast.SourceFile) map[string]*ast.Node {
	result := make(map[string]*ast.Node)
	for _, stmt := range sf.Statements.Nodes {
		switch stmt.Kind {
		case ast.KindFunctionDeclaration, ast.KindClassDeclaration,
			ast.KindInterfaceDeclaration, ast.KindTypeAliasDeclaration,
			ast.KindEnumDeclaration:
			name := stmt.Name()
			if name != nil && ast.IsIdentifier(name) {
				result[name.Text()] = stmt
			}
		case ast.KindVariableStatement:
			vs := stmt.AsVariableStatement()
			if vs.DeclarationList != nil {
				dl := vs.DeclarationList.AsVariableDeclarationList()
				if dl.Declarations != nil {
					for _, decl := range dl.Declarations.Nodes {
						name := decl.Name()
						if name != nil && ast.IsIdentifier(name) {
							result[name.Text()] = stmt
						}
					}
				}
			}
		}
	}
	return result
}

// extractRuntimeText walks an AST statement and produces a text representation
// with all type-only constructs removed. This allows comparing whether two versions
// of a symbol differ only in type annotations.
//
// Type constructs stripped:
//   - Type annotations on variables (`: Type`)
//   - Return type annotations on functions
//   - Type parameters (`<T extends Foo>`)
//   - `as Type` expressions (keep the expression, strip the cast)
//   - `satisfies Type` expressions (keep the expression, strip the check)
//   - `<Type>expr` type assertions (keep the expression)
func extractRuntimeText(stmt *ast.Node, sourceText string) string {
	// Collect all type-only ranges within this statement
	typeRanges := collectTypeOnlyRanges(stmt)

	// Extract the statement's full text
	stmtStart := stmt.Pos()
	stmtEnd := stmt.End()
	if stmtStart < 0 || stmtEnd > len(sourceText) {
		return ""
	}
	fullText := sourceText[stmtStart:stmtEnd]

	// Strip the type ranges (adjust positions relative to statement start)
	return normalizeWhitespace(stripRanges(fullText, typeRanges, stmtStart))
}

// collectTypeOnlyRanges walks the AST node tree and collects [start, end) positions
// of all type-only constructs.
func collectTypeOnlyRanges(node *ast.Node) [][2]int {
	var ranges [][2]int

	var walk func(n *ast.Node)
	walk = func(n *ast.Node) {
		if n == nil {
			return
		}
		switch n.Kind {
		case ast.KindAsExpression:
			// `expr as Type` — keep expr, strip ` as Type`
			ae := n.AsAsExpression()
			if ae.Expression != nil && ae.Type != nil {
				ranges = append(ranges, [2]int{ae.Expression.End(), n.End()})
			}
			// Recurse into the expression (it might contain more type casts)
			if ae.Expression != nil {
				walk(ae.Expression)
			}
			return // don't recurse into Type

		case ast.KindSatisfiesExpression:
			// `expr satisfies Type` — keep expr, strip ` satisfies Type`
			se := n.AsSatisfiesExpression()
			if se.Expression != nil && se.Type != nil {
				ranges = append(ranges, [2]int{se.Expression.End(), n.End()})
			}
			if se.Expression != nil {
				walk(se.Expression)
			}
			return

		case ast.KindTypeAssertionExpression:
			// `<Type>expr` — strip `<Type>`, keep expr
			tae := n.AsTypeAssertion()
			if tae.Expression != nil {
				ranges = append(ranges, [2]int{n.Pos(), tae.Expression.Pos()})
				walk(tae.Expression)
			}
			return
		}

		// For declarations, strip type annotations
		switch n.Kind {
		case ast.KindVariableDeclaration:
			vd := n.AsVariableDeclaration()
			if vd.Type != nil {
				// `: Type` — from colon to start of initializer or end of declaration
				colonEnd := vd.Type.End()
				ranges = append(ranges, [2]int{vd.Name().End(), colonEnd})
			}

		case ast.KindFunctionDeclaration:
			fd := n.AsFunctionDeclaration()
			// Strip type parameters
			if fd.TypeParameters != nil && len(fd.TypeParameters.Nodes) > 0 {
				first := fd.TypeParameters.Nodes[0]
				last := fd.TypeParameters.Nodes[len(fd.TypeParameters.Nodes)-1]
				// Include the angle brackets: one char before first, one char after last
				ranges = append(ranges, [2]int{first.Pos() - 1, last.End() + 1})
			}
			// Strip return type
			if fd.Type != nil {
				ranges = append(ranges, [2]int{fd.Parameters.End(), fd.Type.End()})
			}

		case ast.KindParameter:
			p := n.AsParameterDeclaration()
			if p.Type != nil {
				ranges = append(ranges, [2]int{p.Name().End(), p.Type.End()})
			}

		case ast.KindArrowFunction:
			af := n.AsArrowFunction()
			if af.TypeParameters != nil && len(af.TypeParameters.Nodes) > 0 {
				first := af.TypeParameters.Nodes[0]
				last := af.TypeParameters.Nodes[len(af.TypeParameters.Nodes)-1]
				ranges = append(ranges, [2]int{first.Pos() - 1, last.End() + 1})
			}
			if af.Type != nil {
				ranges = append(ranges, [2]int{af.Parameters.End(), af.Type.End()})
			}
		}

		// Recurse into children
		n.ForEachChild(func(child *ast.Node) bool {
			walk(child)
			return false
		})
	}

	walk(node)
	return ranges
}

// stripRanges removes the given [start, end) byte ranges from the text.
// Ranges are absolute positions; offset is subtracted to make them relative to text start.
func stripRanges(text string, ranges [][2]int, offset int) string {
	if len(ranges) == 0 {
		return text
	}

	// Sort ranges by start position
	sorted := make([][2]int, len(ranges))
	copy(sorted, ranges)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j][0] < sorted[i][0] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	var b strings.Builder
	pos := 0
	for _, r := range sorted {
		start := r[0] - offset
		end := r[1] - offset
		if start < 0 {
			start = 0
		}
		if end > len(text) {
			end = len(text)
		}
		if start < pos {
			start = pos
		}
		if start > pos {
			b.WriteString(text[pos:start])
		}
		pos = end
	}
	if pos < len(text) {
		b.WriteString(text[pos:])
	}
	return b.String()
}

// normalizeWhitespace collapses all whitespace runs to single spaces and trims.
func normalizeWhitespace(s string) string {
	var b strings.Builder
	inSpace := false
	for _, ch := range s {
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(ch)
			inSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}
