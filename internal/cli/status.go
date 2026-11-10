package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"time"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/connect"
	"github.com/waltzofpearls/deepcove/internal/service"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// statusProbeTimeout bounds each question status asks, so a stuck deepcove cannot hang it. Peers get 2s
// each inside the every-node listing.
const statusProbeTimeout = 10 * time.Second

// statusReport is what `deepcove status` shows. `service status` and `network status` keep the details.
type statusReport struct {
	isRunning            bool
	isNotAnswering       bool
	isServiceRunning     bool
	version              string
	isService            bool
	startCommand         string
	network              *connect.Status
	sessionError         string
	localSessionCount    int
	isEveryNodeListed    bool
	everySessionCount    int
	unreachableNodeCount int
	logCommand           []string
}

func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var serviceStatus service.Status
	if manager, err := newServiceManager(ctx); err == nil {
		serviceStatus, _ = manager.Status()
	}
	// Only the socket, because the TCP port can belong to another app, and deepcove answers the CLI
	// there with 401 anyway. One timeout per request, so a slow session listing still leaves time to
	// ask about the network.
	httpClient := socketHTTPClient(socketPath(cfg.DataDir))
	httpClient.Timeout = statusProbeTimeout
	client := &apiClient{baseURL: "http://unix", http: httpClient}
	fmt.Print(joinStringLines(buildStatusLines(findStatusReport(ctx, client, serviceStatus))))
	return nil
}

func findStatusReport(ctx context.Context, client *apiClient, serviceStatus service.Status) statusReport {
	report := statusReport{startCommand: findStartCommand()}
	// Installing again would drop the flags it was installed with.
	if serviceStatus.Installed {
		report.startCommand = "deepcove service start"
	}
	health, err := client.Health(ctx)
	if err != nil {
		var netErr net.Error
		report.isNotAnswering = errors.As(err, &netErr) && netErr.Timeout()
		// It can be starting, or run with another data folder, so only say what is known.
		report.isServiceRunning = serviceStatus.Running
		return report
	}
	report.isRunning = true
	report.version = health.Version
	// Another deepcove can run next to the service with its own data folder.
	if serviceStatus.Running && serviceStatus.PID == health.PID {
		report.isService = true
		report.logCommand = serviceStatus.LogCommand
	}
	if sessions, err := client.List(ctx, false); err != nil {
		report.sessionError = err.Error()
	} else {
		report.localSessionCount = len(sessions)
		// The same listing as `deepcove ls`, so the two agree.
		if listed, err := client.ListEveryNode(ctx, false); err == nil {
			report.isEveryNodeListed = true
			report.everySessionCount = len(listed.Sessions)
			report.unreachableNodeCount = len(listed.Unreachable)
		}
	}
	if network, err := client.NetworkStatus(ctx); err == nil {
		report.network = network
	}
	return report
}

func buildStatusLines(report statusReport) []string {
	if report.isNotAnswering {
		return []string{"deepcove:  running but not answering, see `deepcove service status` for its logs"}
	}
	if report.isServiceRunning {
		return []string{"deepcove:  not answering here, but the service is running, see `deepcove service status`"}
	}
	if !report.isRunning {
		return []string{
			"deepcove:  not running",
			"start it:  " + report.startCommand,
		}
	}
	how := "in the foreground"
	if report.isService {
		how = "as a service"
	}
	lines := []string{
		fmt.Sprintf("deepcove:  running %s, %s", how, report.version),
		"open it:   deepcove open",
	}
	if report.network != nil {
		switch report.network.Phase {
		case string(tailnet.PhaseRunning):
			lines = append(lines,
				fmt.Sprintf("network:   %s (%s)", report.network.Name, report.network.Tailnet),
				"url:       "+report.network.URL)
		default:
			lines = append(lines, "network:   "+report.network.Phase)
		}
	}
	if report.sessionError != "" {
		lines = append(lines, "sessions:  could not list them: "+report.sessionError)
	} else {
		lines = append(lines, "sessions:  "+buildSessionCount(report))
	}
	if len(report.logCommand) > 0 {
		lines = append(lines, "read logs: "+buildShellCommand(report.logCommand))
	}
	return lines
}

func buildSessionCount(report statusReport) string {
	if !report.isEveryNodeListed {
		return fmt.Sprintf("%d running on this node", report.localSessionCount)
	}
	count := fmt.Sprintf("%d running, %d on this node", report.everySessionCount, report.localSessionCount)
	if report.unreachableNodeCount > 0 {
		count += fmt.Sprintf(", %d node(s) did not answer", report.unreachableNodeCount)
	}
	return count
}
