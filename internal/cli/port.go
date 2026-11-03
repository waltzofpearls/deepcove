package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/waltzofpearls/deepcove/internal/config"
)

// portSearchLimit is how many ports past a taken one deepcove tries before it gives up looking.
const portSearchLimit = 100

// windowsAddrInUse is the error code Windows returns for a port in use. Go has no constant for it.
const windowsAddrInUse = syscall.Errno(10048)

func isPortInUseError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EADDRINUSE || (runtime.GOOS == "windows" && errno == windowsAddrInUse)
}

// buildPortInUseErrorFrom says which port is taken and how to move deepcove off it. An --addr beats ADDR,
// so a port given that way is moved the same way.
func buildPortInUseErrorFrom(listenAddr string, startArgs []string) error {
	freeAddr, found := findNextFreeAddr(listenAddr)
	if _, hasAddrFlag := findAddrFlagIn(startArgs); hasAddrFlag {
		if !found {
			return fmt.Errorf("port %s is used by another app. Move deepcove to another port with --addr", extractPortFrom(listenAddr))
		}
		return fmt.Errorf("port %s is used by another app. Move deepcove to a free port with --addr %s", extractPortFrom(listenAddr), freeAddr)
	}
	if !found {
		return fmt.Errorf("port %s is used by another app. Move deepcove to another port with `deepcove config set ADDR=...`", extractPortFrom(listenAddr))
	}
	return fmt.Errorf("port %s is used by another app. Move deepcove to a free port:\n"+
		"  deepcove config set ADDR=%s\n"+
		"Then run `%s`",
		extractPortFrom(listenAddr), freeAddr, findStartCommand())
}

// isPortTakenByAnotherApp is true when something other than this deepcove holds the port it listens on.
func isPortTakenByAnotherApp(listenAddr, dataDir string) bool {
	listener, err := net.Listen("tcp", listenAddr)
	if err == nil {
		_ = listener.Close()
		return false
	}
	if !isPortInUseError(err) {
		return false
	}
	return !isPortServedByRunningDeepcove(listenAddr, socketPath(dataDir))
}

// findAddrFlagIn is the --addr in the arguments of `deepcove start`, which beats ADDR. Like Go's flag
// parser, the last one wins.
func findAddrFlagIn(startArgs []string) (string, bool) {
	addr, found := "", false
	for index, arg := range startArgs {
		for _, name := range []string{"-addr", "--addr"} {
			if value, isAddr := strings.CutPrefix(arg, name+"="); isAddr {
				addr, found = value, true
			}
			if arg == name && index+1 < len(startArgs) {
				addr, found = startArgs[index+1], true
			}
		}
	}
	return addr, found
}

// buildStartArgsWithAddr is the service's `deepcove start` arguments with every --addr swapped for
// newAddr. A reinstall keeps only the arguments it is given, so the others must come along.
func buildStartArgsWithAddr(startCommand []string, newAddr string) []string {
	startIndex := slices.Index(startCommand, "start")
	if startIndex < 0 {
		return []string{"--addr", newAddr}
	}
	var startArgs []string
	oldArgs := startCommand[startIndex+1:]
	for index := 0; index < len(oldArgs); index++ {
		arg := oldArgs[index]
		if arg == "-addr" || arg == "--addr" {
			index++
			continue
		}
		if strings.HasPrefix(arg, "-addr=") || strings.HasPrefix(arg, "--addr=") {
			continue
		}
		startArgs = append(startArgs, arg)
	}
	return append(startArgs, "--addr", newAddr)
}

// isPortServedByRunningDeepcove asks the deepcove on sock where it listens. One that answers may listen
// elsewhere while another app holds this address. One too old to say is taken to hold it, so an upgrade
// never moves the port of the deepcove it replaces.
func isPortServedByRunningDeepcove(listenAddr, sock string) bool {
	if !socketAvailable(sock) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+listenAddrPath, nil)
	if err != nil {
		return false
	}
	response, err := socketHTTPClient(sock).Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return true
	}
	var body struct {
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return true
	}
	return isSameListenAddr(body.Addr, listenAddr)
}

// isSameListenAddr is true when a listener on servedAddr holds listenAddr. One on every address, like
// 0.0.0.0, holds them all.
func isSameListenAddr(servedAddr, listenAddr string) bool {
	servedHost, servedPort, err := net.SplitHostPort(servedAddr)
	if err != nil {
		return false
	}
	listenHost, listenPort, err := net.SplitHostPort(listenAddr)
	if err != nil || servedPort != listenPort {
		return false
	}
	if isEveryAddress(servedHost) || isEveryAddress(listenHost) {
		return true
	}
	servedIP, listenIP := net.ParseIP(servedHost), net.ParseIP(listenHost)
	if servedIP != nil && listenIP != nil {
		return servedIP.Equal(listenIP)
	}
	return strings.EqualFold(servedHost, listenHost)
}

func isEveryAddress(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// findNextFreeAddr is listenAddr moved to the first free port after its own.
func findNextFreeAddr(listenAddr string) (string, bool) {
	listenHost, portText, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", false
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", false
	}
	for candidate := port + 1; candidate <= port+portSearchLimit && candidate <= 65535; candidate++ {
		candidateAddr := net.JoinHostPort(listenHost, strconv.Itoa(candidate))
		listener, err := net.Listen("tcp", candidateAddr)
		if err == nil {
			_ = listener.Close()
			return candidateAddr, true
		}
	}
	return "", false
}

// serviceDefaultAddr is where the service listens with no ADDR stored. A var, so tests can use a port
// they hold.
var serviceDefaultAddr = config.DefaultAddr

// findServiceAddr is where the background service listens without --addr. It reads only the settings
// file, since the service never sees what this shell exports.
func findServiceAddr(dataDir string) (addr string, isStored bool, err error) {
	stored, err := config.EnvFileValues(dataDir)
	if err != nil {
		return "", false, err
	}
	for _, entry := range stored {
		if value, found := strings.CutPrefix(entry, "ADDR="); found {
			return value, true, nil
		}
	}
	return serviceDefaultAddr, false, nil
}

// switchToNextFreePortIfTaken stores the next free port when another app already holds the default
// one, so a first install works without a question. A port you chose yourself is never changed. Run it
// after the --env settings are stored.
func switchToNextFreePortIfTaken(cfg *config.Config, startArgs []string) error {
	if _, chosen := findAddrFlagIn(startArgs); chosen {
		return nil
	}
	serviceAddr, chosen, err := findServiceAddr(cfg.DataDir)
	if err != nil || chosen {
		return err
	}
	if !isPortTakenByAnotherApp(serviceAddr, cfg.DataDir) {
		return nil
	}
	// With nothing free nearby, `deepcove start` reports the taken port and how to fix it.
	freeAddr, found := findNextFreeAddr(serviceAddr)
	if !found {
		return nil
	}
	if err := config.SetEnvFileValues(cfg.DataDir, []string{"ADDR=" + freeAddr}); err != nil {
		return err
	}
	// On stderr, since the installers hide what `service install` prints to stdout.
	printWarning(fmt.Sprintf("Port %s is used by another app, so deepcove uses %s instead.", extractPortFrom(serviceAddr), extractPortFrom(freeAddr)))
	return nil
}

// printPortProblem adds the fix to `service status` when another app holds deepcove's port. The port is
// the service's own --addr when it was installed with one, and its stored ADDR otherwise.
func printPortProblem(cfg *config.Config, startCommand []string) {
	listenAddr, hasAddrFlag := findAddrFlagIn(startCommand)
	if !hasAddrFlag {
		serviceAddr, _, err := findServiceAddr(cfg.DataDir)
		if err != nil {
			return
		}
		listenAddr = serviceAddr
	}
	if !isPortTakenByAnotherApp(listenAddr, cfg.DataDir) {
		return
	}
	freeAddr, found := findNextFreeAddr(listenAddr)
	if !found {
		freeAddr = "..."
	}
	fmt.Printf("problem:   port %s is used by another app. Move deepcove to a free port:\n", extractPortFrom(listenAddr))
	if hasAddrFlag {
		fmt.Printf("             deepcove service install -- %s\n", strings.Join(buildStartArgsWithAddr(startCommand, freeAddr), " "))
	} else {
		fmt.Printf("             deepcove config set ADDR=%s\n", freeAddr)
	}
	fmt.Println("             deepcove service restart")
}

// printWarning prints amber on a terminal. Windows consoles may not understand the color codes.
func printWarning(message string) {
	if runtime.GOOS != "windows" && term.IsTerminal(int(os.Stderr.Fd())) {
		message = "\033[33m" + message + "\033[0m"
	}
	_, _ = fmt.Fprintln(os.Stderr, message)
}

func extractPortFrom(listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	return port
}
