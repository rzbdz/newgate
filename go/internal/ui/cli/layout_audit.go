package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/rzbdz/newgate/go/internal/store"
	"github.com/rzbdz/newgate/go/internal/ui/style"
)

type widthViolation struct {
	stream string
	line   int
	width  int
	text   string
}

func shouldAuditLayout(args []string) bool {
	explicit := os.Getenv("NEWGATE_LAYOUT_AUDIT") != ""
	if len(args) == 0 {
		return explicit || store.LoadState().DebugActive()
	}
	switch args[0] {
	case "__serve", "logs", "log", "alllogs", "all-logs", "tui",
		"start", "stop", "restart", "on", "off", "takeover", "take",
		"release", "free", "reload", "init", "run":
		return false
	case "probe":
		if has(args, "--json") {
			return false
		}
	case "profile":
		// `profile kv` 是给管道/文件用的原始配置，不是控制面版式。
		return false
	}
	if _, launching := detectLaunch(args); launching {
		return false
	}
	return explicit || store.LoadState().DebugActive()
}

func auditLayout(args []string, run func() int) int {
	stdout, stderr := os.Stdout, os.Stderr
	outR, outW, outErr := os.Pipe()
	errR, errW, errErr := os.Pipe()
	if outErr != nil || errErr != nil {
		for _, file := range []*os.File{outR, outW, errR, errW} {
			if file != nil {
				_ = file.Close()
			}
		}
		return run()
	}

	os.Stdout, os.Stderr = outW, errW
	results := make(chan []widthViolation, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go auditStream(&wg, outR, stdout, "stdout", results)
	go auditStream(&wg, errR, stderr, "stderr", results)

	code := run()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = stdout, stderr
	wg.Wait()
	close(results)

	command := "newgate"
	if len(args) > 0 {
		command += " " + args[0]
	}
	var violations []widthViolation
	for batch := range results {
		violations = append(violations, batch...)
	}
	for _, v := range violations {
		fmt.Fprintf(stderr, "[layout] %s %s 第 %d 行：%d 列（上限 %d）: %s\n",
			command, v.stream, v.line, v.width, style.MaxColumns, v.text)
	}
	return code
}

func auditStream(wg *sync.WaitGroup, src, dst *os.File, stream string,
	results chan<- []widthViolation) {
	defer wg.Done()
	defer src.Close()
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var violations []widthViolation
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		fmt.Fprintln(dst, text)
		if width := style.VisibleWidth(text); width > style.MaxColumns {
			violations = append(violations, widthViolation{
				stream: stream, line: line, width: width,
				text: strings.TrimSpace(text),
			})
		}
	}
	results <- violations
}
