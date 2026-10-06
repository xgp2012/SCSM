//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The tests use the classic Go re-exec trick: the test binary re-runs itself
// with SC_TEST_HELPER set, so we get a real child process that is not dotnet
// (which does not exist in this sandbox) without shipping a second binary.
//
// Each helper mode below exercises one branch of the runner:
//
//	ready      prints the two real §5.2.1 anchors with ANSI colour, then serves
//	           stdin (echoing "CMD:<line>") until it receives Ctrl+C, at which
//	           point it exits 0 — the "enhanced terminal safe exit" analogue.
//	crash      prints a line, then exits with a non-zero code while Running.
//	no-anchor  prints ordinary lines and never reaches readiness.
//	ignore-cmd ignores /stop and Ctrl+C, forcing the SIGTERM stage.
//	ignore-all ignores /stop, Ctrl+C and SIGTERM, forcing the SIGKILL stage.
//	slow-sub   prints a burst of lines fast, to overflow a slow subscriber.
//	tail       prints a partial line with no newline, then exits, to prove the
//	           flush path does not lose the tail.
//	spawner    starts a `sleep` in the same process group, so the test can prove
//	           SIGKILL to -pgid reaps grandchildren too.
const helperEnv = "SC_TEST_HELPER"

// maybeRunHelper runs the helper described by SC_TEST_HELPER and exits. It
// returns (does not exit) when the process is the real test binary.
func maybeRunHelper() {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	code := runHelper(mode)
	os.Exit(code)
}

func runHelper(mode string) int {
	switch mode {
	case "ready":
		return helperReady(false)
	case "ignore-cmd":
		return helperReady(true)
	case "crash":
		fmt.Println("INFO: 启动中，马上要崩了")
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "ERROR: simulated crash")
		return 7
	case "no-anchor":
		for i := 0; i < 200; i++ {
			fmt.Printf("INFO: 普通日志行 %d，没有任何就绪锚点\n", i)
			time.Sleep(20 * time.Millisecond)
		}
		return 0
	case "ignore-all":
		// Ignore SIGTERM (and Ctrl+C, which the terminal would turn into
		// SIGINT): only SIGKILL can stop this one.
		signal.Ignore(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
		fmt.Println("INFO: ignoring SIGTERM")
		time.Sleep(60 * time.Second)
		return 0
	case "slow-sub":
		for i := 0; i < 5000; i++ {
			fmt.Printf("INFO: burst line %05d %s\n", i, strings.Repeat("x", 80))
		}
		time.Sleep(2 * time.Second)
		return 0
	case "tail":
		fmt.Print("INFO: 最后一行没有换行符")
		return 0
	case "spawner":
		// Start a grandchild that outlives us unless the whole group is killed.
		pid, err := syscall.ForkExec("/bin/sleep", []string{"sleep", "60"}, &syscall.ProcAttr{
			Files: []uintptr{0, 1, 2},
		})
		if err != nil {
			fmt.Println("ERROR: fork sleep failed:", err)
			return 3
		}
		fmt.Printf("INFO: spawned grandchild %d\n", pid)
		time.Sleep(60 * time.Second)
		return 0
	case "fail-exec":
		return 0
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode: "+mode)
		return 2
	}
}

// helperReady is the flagship helper: it emits the exact anchor lines the rule
// table looks for, with real ANSI colour, then reads stdin.
//
// flags: ignoreStop makes it refuse /stop and Ctrl+C so the stop ladder has to
// escalate, which is how the SIGTERM path is tested.
func helperReady(ignoreStop bool) int {
	// Colour the anchors. The PTY path must preserve these bytes end to end.
	const (
		green = "\x1b[32m"
		cyan  = "\x1b[36m"
		red   = "\x1b[31m"
		reset = "\x1b[0m"
	)
	fmt.Printf("%s[StartServer]开启服务器成功，端口 28887%s\n", green, reset)
	fmt.Printf("Loaded world, GameMode=Harmless, StartingPosition=Easy, WorldName=ShowNameX, VisibilityRange=128, Resolution=High\n")
	fmt.Printf("%sEntered screen \"Game\"%s\n", cyan, reset)
	fmt.Printf("开始加载插件 TestPlugin(tests/TestPlugin.dll) 版本: 1:0:0\n")

	// SIGINT arrives as Ctrl+C on the PTY; exit cleanly like the server's
	// 增强终端安全退出 path does.
	intCh := make(chan os.Signal, 1)
	signal.Notify(intCh, syscall.SIGINT)
	if ignoreStop {
		signal.Ignore(syscall.SIGINT)
	}

	// stdin reader: a PTY delivers the parent's writes here.
	lines := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				for {
					i := indexByteHelper(acc, '\n')
					if i < 0 {
						break
					}
					line := strings.TrimRight(string(acc[:i]), "\r")
					acc = acc[i+1:]
					lines <- line
				}
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return 0
			}
			// Echo, exactly as a terminal would, so tests also cover the
			// "our own command appears in the log" case.
			fmt.Printf("%s[console]: %s%s\n", red, line, reset)
			if line == "/stop" && !ignoreStop {
				fmt.Println("INFO: 收到 /stop，正在保存并退出")
				return 0
			}
			fmt.Printf("[cmd]: executed %q\n", line)
		case <-intCh:
			fmt.Println("INFO: 收到 Ctrl+C，增强终端安全退出")
			return 0
		}
	}
}

func indexByteHelper(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// helperEnvFor builds the child environment for a helper invocation.
func helperEnvFor(mode string) []string {
	return []string{
		helperEnv + "=" + mode,
		// Deterministic, C locale so the Chinese anchors round-trip byte-exactly.
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
}

var _ = strconv.Itoa
