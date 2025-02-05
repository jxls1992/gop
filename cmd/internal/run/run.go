/*
 Copyright 2021 The GoPlus Authors (goplus.org)

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

// Package run implements the ``gop run'' command.
package run

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goplus/gop/ast"
	"github.com/goplus/gop/cl"
	"github.com/goplus/gop/cmd/gengo"
	"github.com/goplus/gop/cmd/internal/base"
	"github.com/goplus/gop/parser"
	"github.com/goplus/gop/scanner"
	"github.com/goplus/gop/token"
	"github.com/goplus/gox"
	"github.com/qiniu/x/log"
	"golang.org/x/tools/go/packages"
)

// -----------------------------------------------------------------------------

// Cmd - gop run
var Cmd = &base.Command{
	UsageLine: "gop run [-asm -quiet -debug -prof] <gopSrcDir|gopSrcFile>",
	Short:     "Run a Go+ program",
}

var (
	flag        = &Cmd.Flag
	flagAsm     = flag.Bool("asm", false, "generates `asm` code of Go+ bytecode backend")
	flagVerbose = flag.Bool("v", false, "print verbose information")
	flagQuiet   = flag.Bool("quiet", false, "don't generate any compiling stage log")
	flagDebug   = flag.Bool("debug", false, "set log level to debug")
	flagProf    = flag.Bool("prof", false, "do profile and generate profile report")
)

func init() {
	Cmd.Run = runCmd
}

func saveGoFile(gofile string, pkg *gox.Package) error {
	dir := filepath.Dir(gofile)
	err := os.MkdirAll(dir, 0777)
	if err != nil {
		return err
	}
	return gox.WriteFile(gofile, pkg, false)
}

func findGoModFile(dir string) (string, error) {
	modfile, err := cl.FindGoModFile(dir)
	if err != nil {
		home := os.Getenv("HOME")
		modfile = home + "/gop/go.mod"
		if fi, e := os.Lstat(modfile); e == nil && !fi.IsDir() {
			return modfile, nil
		}
		modfile = home + "/goplus/go.mod"
		if fi, e := os.Lstat(modfile); e == nil && !fi.IsDir() {
			return modfile, nil
		}
	}
	return modfile, err
}

func findGoModDir(dir string) string {
	modfile, err := findGoModFile(dir)
	if err != nil {
		log.Fatalln("findGoModFile:", err)
	}
	return filepath.Dir(modfile)
}

func runCmd(cmd *base.Command, args []string) {
	flag.Parse(args)
	if flag.NArg() < 1 {
		cmd.Usage(os.Stderr)
	}
	args = flag.Args()[1:]

	if *flagQuiet {
		log.SetOutputLevel(0x7000)
	} else if *flagDebug {
		log.SetOutputLevel(log.Ldebug)
		gox.SetDebug(gox.DbgFlagAll)
		cl.SetDebug(cl.DbgFlagAll)
	}
	if *flagVerbose {
		gox.SetDebug(gox.DbgFlagAll &^ gox.DbgFlagComments)
		cl.SetDebug(cl.DbgFlagAll)
		cl.SetDisableRecover(true)
	} else if *flagAsm {
		gox.SetDebug(gox.DbgFlagInstruction)
	}
	if *flagProf {
		panic("TODO: profile not impl")
	}

	fset := token.NewFileSet()
	src, _ := filepath.Abs(flag.Arg(0))
	isDir, err := IsDir(src)
	if err != nil {
		log.Fatalln("input arg check failed:", err)
	}
	if !isDir && filepath.Ext(src) == ".go" { // not a Go+ file
		runGoFile(src, args)
		return
	}

	var targetDir, file, gofile string
	var pkgs map[string]*ast.Package
	if isDir {
		targetDir = src
		gofile = src + "/gop_autogen.go"
		pkgs, err = parser.ParseDir(fset, src, nil, 0)
	} else {
		targetDir, file = filepath.Split(src)
		targetDir = filepath.Join(targetDir, ".gop")
		gofile = filepath.Join(targetDir, file+".go")
		pkgs, err = parser.Parse(fset, src, nil, 0)
	}
	if err != nil {
		scanner.PrintError(os.Stderr, err)
		os.Exit(10)
	}

	mainPkg, ok := pkgs["main"]
	if !ok {
		if len(pkgs) == 0 { // not a Go+ package, try runGoPkg
			runGoPkg(src, args)
			return
		}
		fmt.Fprintln(os.Stderr, "TODO: not a main package")
		os.Exit(12)
	}

	conf := &cl.Config{
		Dir: findGoModDir(targetDir), TargetDir: targetDir, Fset: fset, CacheLoadPkgs: true}
	out, err := cl.NewPackage("", mainPkg, conf)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(11)
	}
	err = saveGoFile(gofile, out)
	if err != nil {
		log.Fatalln("saveGoFile failed:", err)
	}
	goRun(gofile, args)
	if *flagProf {
		panic("TODO: profile not impl")
	}
}

// IsDir checks a target path is dir or not.
func IsDir(target string) (bool, error) {
	fi, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

func goRun(target string, args []string) {
	dir, file := filepath.Split(target)
	goArgs := make([]string, len(args)+2)
	goArgs[0] = "run"
	goArgs[1] = file
	copy(goArgs[2:], args)
	cmd := exec.Command("go", goArgs...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	err := cmd.Run()
	if err != nil {
		switch e := err.(type) {
		case *exec.ExitError:
			os.Stderr.Write(e.Stderr)
			os.Exit(e.ExitCode())
		default:
			log.Fatalln("go run failed:", err)
		}
	}
}

func runGoPkg(src string, args []string) {
	modfile, err := findGoModFile(src)
	if err != nil {
		log.Fatalln("findGoModFile:", err)
	}
	base := filepath.Dir(modfile)
	rel, _ := filepath.Rel(base, src)
	modPath, err := cl.GetModulePath(modfile)
	if err != nil {
		log.Fatalln("GetModulePath:", err)
	}
	pkgPath := filepath.Join(modPath, rel)
	const (
		loadTypes = packages.NeedImports | packages.NeedDeps | packages.NeedTypes
		loadModes = loadTypes | packages.NeedName | packages.NeedModule
	)
	baseConf := &cl.Config{
		Fset:          token.NewFileSet(),
		GenGoPkg:      new(gengo.Runner).GenGoPkg,
		CacheLoadPkgs: true,
		NoFileLine:    true,
	}
	loadConf := &packages.Config{Mode: loadModes, Fset: baseConf.Fset}
	pkgs, err := baseConf.Ensure().PkgsLoader.Load(loadConf, pkgPath)
	if err != nil || len(pkgs) == 0 {
		log.Fatalln("PkgsLoader.Load failed:", err)
	}
	if pkgs[0].Name != "main" {
		fmt.Fprintln(os.Stderr, "TODO: not a main package")
		os.Exit(12)
	}
	goRun(src+"/.", args)
}

func runGoFile(src string, args []string) {
	targetDir, file := filepath.Split(src)
	targetDir = filepath.Join(targetDir, ".gop", strings.TrimSuffix(file, ".go"))
	gofile := filepath.Join(targetDir, "gop_autogen.go")
	b, err := ioutil.ReadFile(src)
	if err != nil {
		log.Fatalln("ReadFile failed:", err)
	}
	os.MkdirAll(targetDir, 0777)
	err = ioutil.WriteFile(gofile, b, 0666)
	if err != nil {
		log.Fatalln("WriteFile failed:", err)
	}
	runGoPkg(targetDir, args)
}

// -----------------------------------------------------------------------------
