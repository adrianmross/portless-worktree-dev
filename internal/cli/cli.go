package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type commandError struct {
	msg string
	err error
}

func (e commandError) Error() string {
	if e.err == nil {
		return e.msg
	}
	return e.msg + ": " + e.err.Error()
}

func (e commandError) Unwrap() error { return e.err }

func FprintError(w io.Writer, err error) {
	fmt.Fprintf(w, "portless-worktree-dev: %v\n", err)
}

type config struct {
	appName            string
	devCommand         []string
	branch             string
	readyTimeout       time.Duration
	readyInterval      time.Duration
	startGrace         time.Duration
	requiredNodeMajors map[string]bool
	jsonOutput         bool
	requireDefaultURL  bool
}

type statusReport struct {
	AppName             string   `json:"appName"`
	Branch              string   `json:"branch"`
	RepoRoot            string   `json:"repoRoot"`
	StateDir            string   `json:"stateDir"`
	URL                 string   `json:"url,omitempty"`
	PID                 int      `json:"pid,omitempty"`
	Running             bool     `json:"running"`
	Ready               bool     `json:"ready"`
	Stale               bool     `json:"stale"`
	DevCommand          []string `json:"devCommand"`
	RequireDefaultProxy bool     `json:"requireDefaultProxy"`
}

type packageJSON struct {
	Name string `json:"name"`
}

func Run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printUsage(os.Stdout)
		return nil
	}

	command := args[0]
	args = args[1:]

	cfg, passthrough, err := parseConfig(args)
	if err != nil {
		return err
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return commandError{"change to repository root", err}
	}

	if commandNeedsRepoRuntime(command) {
		if err := ensureRuntime(cfg, append([]string{command}, args...)); err != nil {
			return err
		}
	}

	switch command {
	case "install":
		return installDeps()
	case "run":
		return runDev(cfg, passthrough)
	case "start":
		return startDev(cfg, root, passthrough)
	case "restart":
		if err := stopDev(cfg, root); err != nil {
			return err
		}
		return startDev(cfg, root, passthrough)
	case "stop":
		return stopDev(cfg, root)
	case "status":
		return statusDev(cfg, root)
	case "url":
		url, err := printURL(cfg)
		if err != nil {
			return err
		}
		fmt.Println(url)
		return nil
	case "logs":
		return tailLogs(cfg, root)
	case "typecheck", "build":
		return runPackageScript(command)
	default:
		printUsage(os.Stderr)
		return commandError{msg: "unknown command: " + command}
	}
}

func commandNeedsRepoRuntime(command string) bool {
	switch command {
	case "install", "run", "start", "restart", "typecheck", "build":
		return true
	default:
		return false
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: portless-worktree-dev <command> [options] [-- dev-command...]

Commands:
  install       Install pnpm dependencies for this worktree.
  run           Run the Portless dev server in the foreground.
  start         Start the Portless dev server in the background if needed.
  restart       Stop and start the background dev server.
  stop          Stop the background dev server for this worktree.
  status        Show watcher status and active Portless routes.
  url           Print the Portless URL for this worktree.
  logs          Tail the background dev server log.
  typecheck     Run TypeScript validation in the repo runtime.
  build         Run the production build in the repo runtime.

Options:
  --branch <name>  Use a branch name for state lookup, mostly for WorkTrunk post-remove.
  --json           Emit machine-readable JSON for status.

Environment:
  PORTLESS_APP_NAME                  Portless route name. Defaults to package.json name.
  PORTLESS_DEV_COMMAND              Foreground dev command. Defaults to "next dev".
  PORTLESS_READY_TIMEOUT_SECONDS    Seconds to wait for the Portless URL. Defaults to 30.
  PORTLESS_REQUIRED_NODE_MAJORS     Space-separated Node majors. Defaults to "22 24".
  PORTLESS_REQUIRE_DEFAULT_PROXY    Refuse URLs with explicit ports such as :1355.

Examples:
  portless-worktree-dev start
  PORTLESS_APP_NAME=my-app portless-worktree-dev run -- vite --host 127.0.0.1
`)
}

func parseConfig(args []string) (config, []string, error) {
	cfg := config{
		readyTimeout:       secondsEnv("PORTLESS_READY_TIMEOUT_SECONDS", 30),
		readyInterval:      secondsEnv("PORTLESS_READY_INTERVAL_SECONDS", 1),
		startGrace:         secondsEnv("PORTLESS_START_GRACE_SECONDS", 1),
		requiredNodeMajors: requiredNodeMajors(),
		requireDefaultURL:  truthyEnv("PORTLESS_REQUIRE_DEFAULT_PROXY"),
	}

	var passthrough []string
	for len(args) > 0 {
		arg := args[0]
		switch arg {
		case "--branch":
			if len(args) < 2 || args[1] == "" {
				return cfg, nil, commandError{msg: "--branch requires a value"}
			}
			cfg.branch = args[1]
			args = args[2:]
		case "--json":
			cfg.jsonOutput = true
			args = args[1:]
		case "--require-default-proxy":
			cfg.requireDefaultURL = true
			args = args[1:]
		case "--":
			passthrough = args[1:]
			args = nil
		default:
			if strings.HasPrefix(arg, "--") {
				return cfg, nil, commandError{msg: "unknown option: " + arg}
			}
			passthrough = args
			args = nil
		}
	}

	if app := os.Getenv("PORTLESS_APP_NAME"); app != "" {
		cfg.appName = app
	} else {
		app, err := packageName()
		if err != nil {
			return cfg, nil, err
		}
		cfg.appName = safeKey(app)
	}

	if len(passthrough) > 0 {
		cfg.devCommand = passthrough
	} else if envCommand := strings.Fields(os.Getenv("PORTLESS_DEV_COMMAND")); len(envCommand) > 0 {
		cfg.devCommand = envCommand
	} else {
		cfg.devCommand = []string{"next", "dev"}
	}

	return cfg, passthrough, nil
}

func truthyEnv(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func secondsEnv(name string, fallback int) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return time.Duration(fallback) * time.Second
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return time.Duration(fallback) * time.Second
	}
	return time.Duration(parsed) * time.Second
}

func requiredNodeMajors() map[string]bool {
	raw := os.Getenv("PORTLESS_REQUIRED_NODE_MAJORS")
	if raw == "" {
		raw = "22 24"
	}
	majors := map[string]bool{}
	for _, major := range strings.Fields(raw) {
		majors[major] = true
	}
	return majors
}

func repoRoot() (string, error) {
	out, err := runOutput("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", commandError{"not inside a git worktree", err}
	}
	return strings.TrimSpace(out), nil
}

func packageName() (string, error) {
	data, err := os.ReadFile("package.json")
	if err != nil {
		root, rootErr := repoRoot()
		if rootErr != nil {
			return "", rootErr
		}
		return filepath.Base(root), nil
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", commandError{"parse package.json", err}
	}
	if pkg.Name == "" {
		root, err := repoRoot()
		if err != nil {
			return "", err
		}
		return filepath.Base(root), nil
	}
	return pkg.Name, nil
}

var safeKeyPattern = regexp.MustCompile(`[^[:alnum:]._-]+`)

func safeKey(value string) string {
	value = strings.ReplaceAll(value, "/", "-")
	value = strings.ReplaceAll(value, `\`, "-")
	value = safeKeyPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "detached"
	}
	return value
}

func currentBranch(root string) string {
	out, err := runOutput("git", "-C", root, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func stateDir(root string, branch string) (string, error) {
	if branch == "" {
		branch = currentBranch(root)
	}
	out, err := runOutput("git", "-C", root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", commandError{"resolve git common dir", err}
	}
	commonDir := strings.TrimSpace(out)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	return filepath.Join(commonDir, "wt", "portless-dev", safeKey(defaultString(branch, "detached"))), nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func ensureRuntime(cfg config, originalArgs []string) error {
	if runtimeOK(cfg) {
		return nil
	}
	if os.Getenv("PORTLESS_WORKTREE_DEV_IN_NIX") == "1" {
		return commandError{msg: "required Node runtime is unavailable even after loading the Nix dev env"}
	}
	if _, err := os.Stat("flake.nix"); err == nil {
		if _, err := exec.LookPath("nix"); err == nil {
			devEnv, err := runOutput("nix", "build", "--option", "warn-dirty", "false", "--no-link", "--print-out-paths", ".#dev-env")
			if err != nil {
				return commandError{"build Nix dev env", err}
			}
			devEnv = strings.TrimSpace(devEnv)
			env := os.Environ()
			env = append(env, "PORTLESS_WORKTREE_DEV_IN_NIX=1")
			env = appendEnvPath(env, filepath.Join(devEnv, "bin"))
			env = append(env, "XDG_DATA_DIRS="+filepath.Join(devEnv, "share")+prefixEnv("XDG_DATA_DIRS", ":"))
			if os.Getenv("NEXT_TELEMETRY_DISABLED") == "" {
				env = append(env, "NEXT_TELEMETRY_DISABLED=1")
			}
			cmd := exec.Command(os.Args[0], originalArgs...)
			cmd.Env = env
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}
	return commandError{msg: "corepack with an allowed Node runtime is required; run from nix develop or direnv"}
}

func runtimeOK(cfg config) bool {
	major, err := nodeMajor()
	if err != nil {
		return false
	}
	return cfg.requiredNodeMajors[major]
}

func nodeMajor() (string, error) {
	out, err := runOutput("node", "-p", "process.versions.node.split('.')[0]")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func appendEnvPath(env []string, dir string) []string {
	path := dir + prefixEnv("PATH", string(os.PathListSeparator))
	return append(env, "PATH="+path)
}

func prefixEnv(name, sep string) string {
	value := os.Getenv(name)
	if value == "" {
		return ""
	}
	return sep + value
}

func depsInstalled() bool {
	checks := []string{
		"node_modules/.pnpm-workspace-state-v1.json",
		"node_modules/.pnpm",
		"node_modules/.bin/portless",
	}
	for _, check := range checks {
		info, err := os.Stat(check)
		if err != nil {
			return false
		}
		if check == "node_modules/.bin/portless" && info.Mode()&0111 == 0 {
			return false
		}
	}
	return true
}

func installDeps() error {
	if depsInstalled() {
		return nil
	}
	if _, err := exec.LookPath("corepack"); err != nil {
		return commandError{"corepack is required to install dependencies", err}
	}
	if err := runInherit("corepack", "pnpm", "install", "--frozen-lockfile"); err != nil {
		return runInherit("corepack", "pnpm", "install", "--frozen-lockfile", "--config.configDependencies=false")
	}
	return nil
}

func portlessArgs(args ...string) (string, []string) {
	if info, err := os.Stat("node_modules/.bin/portless"); err == nil && info.Mode()&0111 != 0 {
		return "node_modules/.bin/portless", args
	}
	return "corepack", append([]string{"pnpm", "exec", "portless"}, args...)
}

func runPortless(args ...string) error {
	bin, argv := portlessArgs(args...)
	return runInherit(bin, argv...)
}

func portlessOutput(args ...string) (string, error) {
	bin, argv := portlessArgs(args...)
	return runOutput(bin, argv...)
}

func printURL(cfg config) (string, error) {
	if err := installDeps(); err != nil {
		return "", err
	}
	out, err := portlessOutput("get", cfg.appName)
	if err != nil {
		return "", commandError{"get Portless URL", err}
	}
	resolved := strings.TrimSpace(out)
	if err := validatePortlessURL(cfg, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func validatePortlessURL(cfg config, resolved string) error {
	if !cfg.requireDefaultURL {
		return nil
	}
	parsed, err := url.Parse(resolved)
	if err != nil {
		return commandError{"parse Portless URL", err}
	}
	if parsed.Port() == "" {
		return nil
	}
	return commandError{msg: fmt.Sprintf(`Portless default HTTPS proxy is required, but the resolved URL includes an explicit port: %s

Start or repair the default HTTPS proxy from an interactive terminal:
  PORTLESS_PORT=443 corepack pnpm exec portless proxy start --https

If that reports stale or root-owned state, restart the proxy:
  PORTLESS_PORT=443 corepack pnpm exec portless proxy stop
  PORTLESS_PORT=443 corepack pnpm exec portless proxy start --https

The proxy must listen on HTTPS port 443 for URLs without a port suffix.`, resolved)}
}

func runDev(cfg config, passthrough []string) error {
	if err := installDeps(); err != nil {
		return err
	}
	command := cfg.devCommand
	if len(passthrough) > 0 {
		command = passthrough
	}
	args := append([]string{"run", "--name", cfg.appName, "--force"}, command...)
	bin, argv := portlessArgs(args...)
	return runInherit(bin, argv...)
}

func startDev(cfg config, root string, passthrough []string) error {
	if err := installDeps(); err != nil {
		return err
	}
	dir, err := stateDir(root, cfg.branch)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return commandError{"create state dir", err}
	}
	pidfile := filepath.Join(dir, "pid")
	logfile := filepath.Join(dir, "dev.log")

	if pid, err := readPID(pidfile); err == nil && processRunning(pid) {
		url, err := printURL(cfg)
		if err == nil && urlReady(url) {
			fmt.Printf("Portless dev server already running (pid %d) at %s\n", pid, url)
			return nil
		}
		fmt.Printf("Portless dev server pid %d is running but not ready; restarting\n", pid)
		if err := stopDev(cfg, root); err != nil {
			return err
		}
	}

	log, err := os.Create(logfile)
	if err != nil {
		return commandError{"open dev log", err}
	}
	defer log.Close()

	cmdArgs := append([]string{"run"}, passthrough...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Stdin = nil
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return commandError{"start Portless dev server", err}
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0644); err != nil {
		return commandError{"write pid file", err}
	}

	time.Sleep(cfg.startGrace)
	if !processRunning(cmd.Process.Pid) {
		pid, found := findDevPID(root)
		if found {
			_ = os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), 0644)
		} else {
			return logFailure("Portless dev server failed to start", logfile)
		}
	}

	url, err := waitForReadyURL(cfg, cmd.Process.Pid, logfile)
	if err != nil {
		return err
	}
	fmt.Printf("Started Portless dev server (pid %d) at %s\n", cmd.Process.Pid, url)
	fmt.Printf("Log: %s\n", logfile)
	return nil
}

func stopDev(cfg config, root string) error {
	dir, err := stateDir(root, cfg.branch)
	if err != nil {
		return err
	}
	pidfile := filepath.Join(dir, "pid")
	pid, err := readPID(pidfile)
	if err != nil {
		fmt.Printf("No Portless dev server pid file for %s\n", defaultString(cfg.branch, currentBranch(root)))
		return nil
	}
	if processRunning(pid) {
		_ = terminatePID(pid)
		fmt.Printf("Stopped Portless dev server (pid %d)\n", pid)
	} else {
		fmt.Printf("Removed stale Portless dev server pid %d\n", pid)
	}
	_ = os.Remove(pidfile)
	_ = runPortless("prune")
	return nil
}

func statusDev(cfg config, root string) error {
	report, err := collectStatus(cfg, root)
	if err != nil {
		return err
	}
	if cfg.jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return commandError{"encode status JSON", err}
		}
		fmt.Println(string(encoded))
		return nil
	}

	if report.Running {
		fmt.Printf("Portless watcher: running (pid %d)\n", report.PID)
	} else if report.Stale {
		fmt.Printf("Portless watcher: stale pid %d\n", report.PID)
	} else {
		fmt.Println("Portless watcher: stopped")
	}
	if report.URL != "" {
		fmt.Printf("URL: %s\n", report.URL)
	}
	return runPortless("list")
}

func collectStatus(cfg config, root string) (statusReport, error) {
	dir, err := stateDir(root, cfg.branch)
	if err != nil {
		return statusReport{}, err
	}
	branch := cfg.branch
	if branch == "" {
		branch = currentBranch(root)
	}
	report := statusReport{
		AppName:             cfg.appName,
		Branch:              branch,
		RepoRoot:            root,
		StateDir:            dir,
		DevCommand:          cfg.devCommand,
		RequireDefaultProxy: cfg.requireDefaultURL,
	}

	pidfile := filepath.Join(dir, "pid")
	if pid, err := readPID(pidfile); err == nil {
		report.PID = pid
		if processRunning(pid) {
			report.Running = true
		} else if foundPid, found := findDevPID(root); found {
			_ = os.MkdirAll(dir, 0755)
			_ = os.WriteFile(pidfile, []byte(strconv.Itoa(foundPid)+"\n"), 0644)
			report.PID = foundPid
			report.Running = true
		} else {
			report.Stale = true
		}
	} else if foundPid, found := findDevPID(root); found {
		_ = os.MkdirAll(dir, 0755)
		_ = os.WriteFile(pidfile, []byte(strconv.Itoa(foundPid)+"\n"), 0644)
		report.PID = foundPid
		report.Running = true
	}

	if resolved, err := printURL(cfg); err == nil {
		report.URL = resolved
		report.Ready = urlReady(resolved)
	} else if cfg.requireDefaultURL {
		return report, err
	}
	return report, nil
}

func tailLogs(cfg config, root string) error {
	dir, err := stateDir(root, cfg.branch)
	if err != nil {
		return err
	}
	logfile := filepath.Join(dir, "dev.log")
	if _, err := os.Stat(logfile); err != nil {
		return commandError{"no log file at " + logfile, err}
	}
	lines := os.Getenv("PORTLESS_LOG_LINES")
	if lines == "" {
		lines = "120"
	}
	return runInherit("tail", "-n", lines, "-f", logfile)
}

func runPackageScript(name string) error {
	if err := installDeps(); err != nil {
		return err
	}
	return runInherit("corepack", "pnpm", "run", name)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run()
	return err == nil
}

func terminatePID(pid int) error {
	_ = exec.Command("kill", strconv.Itoa(pid)).Run()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
}

func findDevPID(root string) (int, bool) {
	out, err := runOutput("ps", "-axo", "pid=,command=")
	if err != nil {
		return 0, false
	}
	pattern := regexp.MustCompile(`(next|vite|astro|remix|webpack|rspack|turbo)\s+(dev|serve|start)`)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, root) || !pattern.MatchString(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil {
			return pid, true
		}
	}
	return 0, false
}

func waitForReadyURL(cfg config, pid int, logfile string) (string, error) {
	deadline := time.Now().Add(cfg.readyTimeout)
	for time.Now().Before(deadline) || time.Now().Equal(deadline) {
		if !processRunning(pid) {
			return "", logFailure("Portless dev server exited before it was ready", logfile)
		}
		url, err := printURL(cfg)
		if err == nil && url != "" && urlReady(url) {
			return url, nil
		}
		time.Sleep(cfg.readyInterval)
	}
	return "", logFailure(fmt.Sprintf("Portless URL did not become ready within %s", cfg.readyTimeout), logfile)
}

func urlReady(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client := http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

func logFailure(msg string, logfile string) error {
	lines, _ := lastLines(logfile, 80)
	if lines == "" {
		return commandError{msg: msg}
	}
	return commandError{msg: msg + "; last log lines:\n" + lines}
}

func lastLines(path string, count int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n"), nil
}

func runOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "CI=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return stdout.String(), commandError{msg: detail, err: err}
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

func runInherit(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "CI=true")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
