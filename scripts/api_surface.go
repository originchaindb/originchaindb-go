// Extract compiler-parsed exported declarations; implementation bodies are excluded.
package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	set := token.NewFileSet()
	surface := map[string]string{}
	render := func(node any) string {
		var out bytes.Buffer
		if err := printer.Fprint(&out, set, node); err != nil {
			panic(err)
		}
		return strings.Join(strings.Fields(out.String()), " ")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		panic(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			panic(err)
		}
		for _, decl := range file.Decls {
			switch value := decl.(type) {
			case *ast.FuncDecl:
				if !value.Name.IsExported() {
					continue
				}
				name := value.Name.Name
				if value.Recv != nil {
					name = render(value.Recv.List[0].Type) + "." + name
				}
				surface[name] = render(value.Type)
			case *ast.GenDecl:
				for _, spec := range value.Specs {
					switch item := spec.(type) {
					case *ast.TypeSpec:
						if !item.Name.IsExported() {
							continue
						}
						name := item.Name.Name
						if body, ok := item.Type.(*ast.StructType); ok {
							surface[name] = "struct"
							for _, field := range body.Fields.List {
								if len(field.Names) == 0 {
									surface[name+".embedded."+render(field.Type)] = render(field.Type)
								}
								for _, fieldName := range field.Names {
									if fieldName.IsExported() {
										surface[name+"."+fieldName.Name] = render(field.Type)
										if field.Tag != nil {
											surface[name+"."+fieldName.Name] += " " + field.Tag.Value
										}
									}
								}
							}
						} else {
							surface[name] = render(item.Type)
						}
						if item.TypeParams != nil {
							surface[name] += " generics " + render(item.TypeParams)
						}
					case *ast.ValueSpec:
						for _, name := range item.Names {
							if name.IsExported() {
								kind := value.Tok.String()
								if item.Type != nil {
									kind += " " + render(item.Type)
								}
								surface[name.Name] = kind
							}
						}
					}
				}
			}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(surface); err != nil {
		panic(err)
	}
}
