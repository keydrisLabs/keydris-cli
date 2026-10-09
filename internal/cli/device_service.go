package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/keydrisLabs/keydris-cli/internal/config"
	"github.com/keydrisLabs/keydris-cli/internal/node/deviceservice"
)

func runDeviceService(args []string) int {
	if len(args) != 1 || (args[0] != "run" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "usage: keydris device-service run|status")
		return 2
	}
	cfg := config.Load()
	if err := cfg.ValidatePaths(); err != nil {
		fmt.Fprintf(os.Stderr, "keydris device-service: %v\n", err)
		return 1
	}
	if args[0] == "status" {
		return runDeviceServiceStatus(cfg)
	}
	if effectiveUID() != 0 {
		fmt.Fprintln(os.Stderr, "keydris device-service run must be started as root")
		return 1
	}
	store := deviceservice.NewStore(cfg.DeviceServiceDir, 0)
	service, err := deviceservice.NewService(store, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris device-service: %v\n", err)
		return 1
	}
	server, err := deviceservice.Serve(cfg.DeviceServiceSocket, service, deviceservice.ServerOptions{ExpectedUID: 0})
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris device-service: %v\n", err)
		return 1
	}
	defer server.Close()
	fmt.Fprintf(os.Stdout, "keydris device service listening on %s\n", cfg.DeviceServiceSocket)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return 0
}

func runDeviceServiceStatus(cfg *config.Config) int {
	client := deviceservice.SocketClient{Path: cfg.DeviceServiceSocket}
	health, err := client.Health(context.Background())
	if err != nil {
		newUI(os.Stdout).row("inactive", "Device service", err.Error())
		return 1
	}
	detail := fmt.Sprintf("Running (PID %d, protocol v%d, caller UID %d)", health.PID, health.ProtocolVersion, health.CallerUID)
	if health.Enrolled {
		detail += "; enrolled device " + health.DeviceID + ", certificate expires " + health.CertificateExpiry
	} else {
		detail += "; not enrolled"
	}
	newUI(os.Stdout).row("ok", "Device service", detail)
	return 0
}
