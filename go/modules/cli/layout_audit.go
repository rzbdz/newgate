package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/store"

	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
)

type widthViolation struct {
	stream string
	line   int
	width  int
	text   string
}

func shouldAuditLayout(agents agentapi.AgentCatalog, args []string) bool {
	explicit := os.Getenv("NEWGATE_LAYOUT_AUDIT") != ""
	if len(args) == 0 {
		return explicit || store.LoadState().DebugActive()
	}
	switch args[0] {
	case "__serve", "logs", "log", "alllogs", "all-logs", "tui", "menuconfig", "run":
		return false
	case "probe":
		if has(args, "--json") {
			return false
		}
	case "profile":
		// `profile kv` 是给管道/文件用的原始配置，不是控制面版式。
		return false
	}
	if _, launching := detectLaunch(agents, args); launching {
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
	reader := bufio.NewReader(src)
	var violations []widthViolation
	line := 0
	for {
		raw, err := reader.ReadString('\n')
		if raw != "" {
			_, _ = io.WriteString(dst, raw)
			text := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
			line++
			if width := style.VisibleWidth(text); width > style.MaxColumns {
				violations = append(violations, widthViolation{
					stream: stream, line: line, width: width,
					text: strings.TrimSpace(text),
				})
			}
		}
		if err != nil {
			break
		}
	}
	results <- violations
}
