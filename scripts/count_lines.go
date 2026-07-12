package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var excludeDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	"target": true, "__pycache__": true, ".zenmind": true,
}

var counters = map[string]func(string) (code, comment, blank int){
	".go":  countCStyle,
	".rs":  countCStyle,
	".yml": countHashStyle,
	".md":  countMarkdown,
}

func countCStyle(path string) (code, comment, blank int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()

	inBlock := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			blank++
			continue
		}
		if inBlock {
			comment++
			if strings.Contains(line, "*/") {
				inBlock = false
			}
			continue
		}
		if strings.HasPrefix(line, "//") {
			comment++
			continue
		}
		if strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "//*") {
			comment++
			if !strings.Contains(line, "*/") {
				inBlock = true
			}
			continue
		}
		code++
	}
	return
}

func countHashStyle(path string) (code, comment, blank int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			blank++
		} else if strings.HasPrefix(line, "#") {
			comment++
		} else {
			code++
		}
	}
	return
}

func countMarkdown(path string) (code, comment, blank int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			blank++
		} else {
			comment++ // markdown 全算文档/注释
		}
	}
	return
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	type stat struct {
		files, code, comment, blank int
	}
	total := stat{}
	byExt := map[string]*stat{}

	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if excludeDirs[name] || (len(name) > 0 && name[0] == '.') {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(info.Name())
		fn, ok := counters[ext]
		if !ok {
			return nil
		}
		code, comment, blank := fn(path)
		total.files++
		total.code += code
		total.comment += comment
		total.blank += blank

		if byExt[ext] == nil {
			byExt[ext] = &stat{}
		}
		s := byExt[ext]
		s.files++
		s.code += code
		s.comment += comment
		s.blank += blank
		return nil
	})

	fmt.Println(strings.Repeat("=", 64))
	fmt.Println("项目代码统计")
	fmt.Println(strings.Repeat("=", 64))
	fmt.Printf("%-8s %6s %8s %8s %8s %8s\n", "类型", "文件数", "代码行", "注释行", "空白行", "总行数")
	fmt.Println(strings.Repeat("-", 64))

	for _, ext := range []string{".go", ".rs", ".yml", ".md"} {
		s := byExt[ext]
		if s == nil {
			continue
		}
		t := s.code + s.comment + s.blank
		fmt.Printf("%-8s %6d %8d %8d %8d %8d\n", ext, s.files, s.code, s.comment, s.blank, t)
	}

	all := total.code + total.comment + total.blank
	fmt.Println(strings.Repeat("-", 64))
	fmt.Printf("%-8s %6d %8d %8d %8d %8d\n", "合计", total.files, total.code, total.comment, total.blank, all)
	fmt.Println(strings.Repeat("=", 64))

	srcFiles := total.files
	if s, ok := byExt[".md"]; ok {
		srcFiles -= s.files
	}
	srcCode := total.code
	srcComment := total.comment
	srcBlank := total.blank
	if s, ok := byExt[".md"]; ok {
		srcCode -= s.code
		srcComment -= s.comment
		srcBlank -= s.blank
	}
	srcAll := srcCode + srcComment + srcBlank
	fmt.Println()
	fmt.Println("源代码文件（.go + .rs + .yml，不含文档）:")
	fmt.Printf("  文件数: %d, 代码行: %d, 注释行: %d, 空白行: %d, 总行数: %d\n",
		srcFiles, srcCode, srcComment, srcBlank, srcAll)
}
