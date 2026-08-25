package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/RC-CHN/wg-quic/internal/app"
	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/quick"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

var version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wg-quic-quick:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "broker-service":
		if len(args) != 1 {
			return usage()
		}
		return runManagementService()
	case "desktop-broker-status":
		if len(args) != 1 {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		status, err := runDesktopBrokerStatus(ctx)
		if err != nil {
			return err
		}
		fmt.Println(status)
		return nil
	case "desktop-import":
		name, source, overwrite, err := parseDesktopImportArgs(args[1:])
		if err != nil {
			return usage()
		}
		return quick.ImportDesktopConfig(source, name, overwrite)
	case "desktop-delete":
		if len(args) != 2 {
			return usage()
		}
		return quick.DeleteDesktopConfig(args[1])
	case "desktop-read":
		if len(args) != 2 {
			return usage()
		}
		contents, err := quick.ReadDesktopConfig(args[1])
		if err != nil {
			return err
		}
		fmt.Print(contents)
		return nil
	case "desktop-genkey":
		if len(args) != 1 {
			return usage()
		}
		keys, err := quick.GenerateDesktopKeys()
		if err != nil {
			return err
		}
		fmt.Println(keys)
		return nil
	case "desktop-client":
		request, err := parseDesktopClientArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		message, err := runDesktopClient(ctx, request)
		if err != nil {
			return err
		}
		if message != "" {
			if request.action == "read" {
				fmt.Print(message)
			} else {
				fmt.Println(message)
			}
		}
		return nil
	case "desktop-helper":
		pipePath, err := parseDesktopHelperArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		return runDesktopHelper(ctx, pipePath)
	case "run", "debug":
		input, name, brokerSafe, err := parseQuickRunArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		if args[0] == "debug" {
			if brokerSafe {
				return usage()
			}
			return runQuickDebug(ctx, input, name)
		}
		return runQuick(ctx, input, name, brokerSafe)
	case "check":
		if len(args) != 2 {
			return usage()
		}
		if err := quick.Check(args[1]); err != nil {
			return err
		}
		fmt.Println("configuration is valid for wg-quic-quick")
		return nil
	case "reconcile", "reload", "transaction-status", "refresh-endpoints":
		parsed, err := parseRuntimeCommand(args[0], args[1:])
		if err != nil {
			return err
		}
		ctx, stop := commandContext()
		defer stop()
		return runRuntimeCommand(ctx, args[0], parsed)
	case "show":
		name, jsonOutput, err := app.ParseShowArgs(args[1:])
		if err != nil {
			return err
		}
		if name != "" {
			ctx, stop := commandContext()
			defer stop()
			if status, statusErr := quick.RuntimeStatus(ctx, name); statusErr == nil {
				return printRuntimeStatus(&status, jsonOutput)
			}
		}
		return app.Show(name, jsonOutput)
	case "up":
		if len(args) != 2 {
			return usage()
		}
		return quick.Manage(context.Background(), args[0], args[1])
	case "down":
		name, repair, err := parseQuickDownArgs(args[1:])
		if err != nil {
			return usage()
		}
		if repair {
			result, err := quick.Repair(context.Background(), name)
			if err != nil {
				return err
			}
			if result.ForcedServiceTermination {
				fmt.Printf(
					"repair completed for %s; the stuck service process was explicitly terminated\n",
					name,
				)
			} else {
				fmt.Printf("repair completed for %s\n", name)
			}
			return nil
		}
		return quick.Manage(context.Background(), args[0], name)
	case "version", "--version":
		if len(args) != 1 {
			return usage()
		}
		fmt.Printf("wg-quic-quick %s\n", version)
		return nil
	default:
		return usage()
	}
}

type runtimeCommandArgs struct {
	name               string
	candidatePath      string
	peer               string
	expectedEpoch      string
	expectedGeneration uint64
	requestID          string
	jsonOutput         bool
}

func parseRuntimeCommand(operation string, args []string) (runtimeCommandArgs, error) {
	var result runtimeCommandArgs
	positionals := 1
	if operation == "reconcile" {
		positionals = 2
	}
	if len(args) < positionals {
		return result, fmt.Errorf("%s requires an interface%s", operation, map[bool]string{true: " and candidate path"}[operation == "reconcile"])
	}
	result.name = args[0]
	if result.name == "" {
		return result, errors.New("runtime interface is required")
	}
	if operation == "reconcile" {
		result.candidatePath = args[1]
		if result.candidatePath == "" {
			return result, errors.New("candidate path is required")
		}
	}
	remaining := args[positionals:]
	for len(remaining) != 0 {
		switch remaining[0] {
		case "--json":
			result.jsonOutput = true
			remaining = remaining[1:]
		case "--expected-epoch", "--expected-generation", "--request-id", "--peer":
			if len(remaining) < 2 || remaining[1] == "" {
				return result, fmt.Errorf("%s requires a value", remaining[0])
			}
			option, value := remaining[0], remaining[1]
			remaining = remaining[2:]
			switch option {
			case "--expected-epoch":
				result.expectedEpoch = value
			case "--expected-generation":
				generation, err := strconv.ParseUint(value, 10, 64)
				if err != nil || generation == 0 {
					return result, errors.New("expected generation must be a positive integer")
				}
				result.expectedGeneration = generation
			case "--request-id":
				result.requestID = value
			case "--peer":
				result.peer = value
			}
		default:
			return result, fmt.Errorf("unsupported %s option %q", operation, remaining[0])
		}
	}
	if operation != "refresh-endpoints" && result.peer != "" {
		return result, errors.New("--peer is only valid for refresh-endpoints")
	}
	if operation == "transaction-status" && result.requestID == "" {
		return result, errors.New("transaction-status requires --request-id")
	}
	if operation == "reconcile" &&
		(result.expectedEpoch == "" || result.expectedGeneration == 0) {
		return result, errors.New("reconcile requires --expected-epoch and --expected-generation")
	}
	return result, nil
}

func runRuntimeCommand(ctx context.Context, operation string, args runtimeCommandArgs) error {
	request := management.Request{DeadlineUnixMillis: time.Now().Add(2 * time.Minute).UnixMilli()}
	switch operation {
	case "reconcile":
		request.Operation = management.OperationReconcile
		request.RequiredCapabilities = []string{"peer_reconcile_v1"}
		request.CandidatePath = args.candidatePath
	case "reload":
		request.Operation = management.OperationReload
		request.RequiredCapabilities = []string{"peer_reconcile_v1"}
	case "transaction-status":
		request.Operation = management.OperationTransactionStatus
	case "refresh-endpoints":
		request.Operation = management.OperationRefreshEndpoints
		request.RequiredCapabilities = []string{"endpoint_refresh_v1"}
		request.PublicKey = args.peer
	default:
		return fmt.Errorf("unsupported runtime operation %q", operation)
	}
	request.ExpectedEpoch = args.expectedEpoch
	request.ExpectedGeneration = args.expectedGeneration
	request.RequestID = args.requestID
	if operation == "reload" && (request.ExpectedEpoch == "" || request.ExpectedGeneration == 0) {
		if request.ExpectedEpoch != "" || request.ExpectedGeneration != 0 {
			return errors.New("reload expected epoch and generation must be supplied together")
		}
		status, err := quick.RuntimeStatus(ctx, args.name)
		if err != nil {
			return err
		}
		request.ExpectedEpoch = status.SupervisorEpoch
		request.ExpectedGeneration = status.DesiredGeneration
	}
	if (operation == "reload" || operation == "reconcile") && request.RequestID == "" {
		requestID, err := quick.NewRuntimeRequestID()
		if err != nil {
			return err
		}
		request.RequestID = requestID
	}
	response, err := quick.RuntimeCall(ctx, args.name, request)
	if err != nil {
		return err
	}
	if args.jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(response); err != nil {
			return err
		}
	} else {
		printRuntimeResponse(response)
	}
	if response.Failure != nil {
		return errors.New(response.Failure.Message)
	}
	if response.Result != nil && response.Result.Failure != nil &&
		response.Result.State != reconcile.StateCommitted {
		return errors.New(response.Result.Failure.Message)
	}
	return nil
}

func printRuntimeResponse(response management.Response) {
	switch {
	case response.Result != nil:
		fmt.Printf(
			"%s: generation %d (request %s)\n",
			response.Result.State, response.Result.Generation, response.Result.RequestID,
		)
	case response.OperationResult != nil:
		if response.OperationResult.Peer == "" {
			fmt.Printf("%s completed for %s\n", response.OperationResult.Operation, response.OperationResult.Interface)
		} else {
			fmt.Printf(
				"%s completed for %s peer %s\n",
				response.OperationResult.Operation,
				response.OperationResult.Interface,
				response.OperationResult.Peer,
			)
		}
	case response.Failure != nil:
		fmt.Printf("%s: %s\n", response.Failure.Code, response.Failure.Message)
	}
}

func printRuntimeStatus(status *management.Status, jsonOutput bool) error {
	if status == nil {
		return errors.New("runtime status is unavailable")
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	}
	fmt.Printf(
		"interface: %s\nepoch: %s\ndesired generation: %d\npersistent drift: %t\ncleanup pending: %d\nrecovery: %s (retained ambiguous objects: %d)\ncapabilities: %v\n",
		status.Interface, status.SupervisorEpoch, status.DesiredGeneration,
		status.PersistentDrift, status.CleanupPending, status.Recovery.State,
		status.Recovery.RetainedAmbiguousObjects, status.Capabilities,
	)
	if status.Recovery.Message != "" {
		fmt.Printf("recovery detail: %s\n", status.Recovery.Message)
	}
	if status.CanonicalError != "" {
		fmt.Printf("canonical configuration error: %s\n", status.CanonicalError)
	}
	for _, peer := range status.Peers {
		fmt.Printf(
			"peer %s: configured=%s selected=%s session=%s generation=%d authenticated=%d fec=%s\n",
			peer.PublicKey, peer.ConfiguredEndpoint, peer.SelectedEndpoint,
			peer.Session, peer.EndpointGeneration, peer.AuthenticatedGeneration,
			peer.FECPolicy,
		)
	}
	for _, session := range status.Sessions {
		peerKeys := make([]string, 0, len(session.Peers))
		for _, peer := range session.Peers {
			peerKeys = append(peerKeys, peer.PublicKey)
		}
		fmt.Printf(
			"transport session %d/%d: role=%s state=%s configured=%s current=%s peers=%v cwnd=%d rtt=%dus lost=%d pto=%d queue_drops=%d\n",
			session.SessionID, session.SessionGeneration, session.Role, session.State,
			session.ConfiguredEndpoint, session.CurrentEndpoint, peerKeys,
			session.Stats.QUICCongestionWindowBytes,
			session.Stats.QUICSmoothedRTTUs,
			session.Stats.QUICPacketsLost,
			session.Stats.QUICPTOCount,
			session.Stats.QueueDrops,
		)
	}
	if status.SessionTelemetryOmitted != 0 {
		fmt.Printf("transport sessions omitted: %d\n", status.SessionTelemetryOmitted)
	}
	for _, session := range status.RecentSessions {
		fmt.Printf(
			"closed transport session %d/%d: reason=%s closed=%s replaced_by=%d lost=%d pto=%d queue_drops=%d\n",
			session.SessionID, session.SessionGeneration, session.CloseReason,
			session.ClosedAt.Format(time.RFC3339Nano), session.ReplacedBySessionID,
			session.FinalStats.QUICPacketsLost, session.FinalStats.QUICPTOCount,
			session.FinalStats.QueueDrops,
		)
	}
	if status.RecentSessionsEvicted != 0 {
		fmt.Printf("closed transport sessions evicted: %d\n", status.RecentSessionsEvicted)
	}
	return nil
}

func parseDesktopHelperArgs(args []string) (string, error) {
	if len(args) != 1 || args[0] == "" {
		return "", errors.New("desktop helper pipe is required")
	}
	return args[0], nil
}

func parseDesktopImportArgs(
	args []string,
) (name string, source string, overwrite bool, err error) {
	switch {
	case len(args) == 2 && args[0] != "" && args[1] != "":
		return args[0], args[1], false, nil
	case len(args) == 3 &&
		args[0] != "" &&
		args[1] != "" &&
		args[2] == "--overwrite":
		return args[0], args[1], true, nil
	default:
		return "", "", false, errors.New("invalid desktop import arguments")
	}
}

type desktopClientRequest struct {
	action    string
	name      string
	source    string
	overwrite bool
}

func parseDesktopClientArgs(args []string) (desktopClientRequest, error) {
	if len(args) < 2 {
		return desktopClientRequest{}, errors.New("desktop action and interface are required")
	}
	request := desktopClientRequest{action: args[0], name: args[1]}
	switch request.action {
	case "up", "down", "check", "delete", "read", "status", "reload", "refresh-endpoints":
		if len(args) != 2 {
			return desktopClientRequest{}, errors.New("desktop action received unexpected arguments")
		}
	case "import", "reconcile":
		switch {
		case len(args) == 3:
			request.source = args[2]
		case request.action == "import" && len(args) == 4 && args[3] == "--overwrite":
			request.source = args[2]
			request.overwrite = true
		default:
			return desktopClientRequest{}, errors.New("invalid desktop import arguments")
		}
	default:
		return desktopClientRequest{}, fmt.Errorf("unsupported desktop action %q", request.action)
	}
	if request.name == "" {
		return desktopClientRequest{}, errors.New("desktop interface is required")
	}
	return request, nil
}

func parseQuickRunArgs(
	args []string,
) (input, name string, brokerSafe bool, err error) {
	if len(args) == 0 || args[0] == "" {
		return "", "", false, errors.New(
			"interface or configuration is required",
		)
	}
	input = args[0]
	remaining := args[1:]
	if len(remaining) >= 2 && remaining[0] == "--name" {
		if remaining[1] == "" {
			return "", "", false, errors.New("invalid --name option")
		}
		name = remaining[1]
		remaining = remaining[2:]
	}
	if len(remaining) == 1 && remaining[0] == "--broker-safe" {
		brokerSafe = true
		remaining = nil
	}
	if len(remaining) != 0 {
		return "", "", false, errors.New("invalid run options")
	}
	return input, name, brokerSafe, nil
}

func parseQuickDownArgs(args []string) (name string, repair bool, err error) {
	switch {
	case len(args) == 1 && args[0] != "":
		return args[0], false, nil
	case len(args) == 2 && args[0] != "" && args[1] == "--repair":
		return args[0], true, nil
	default:
		return "", false, errors.New("invalid down arguments")
	}
}

func usage() error {
	fmt.Fprintln(os.Stderr, `Usage:
  wg-quic-quick run INTERFACE|CONFIG [--name INTERFACE]
  wg-quic-quick debug INTERFACE|CONFIG [--name INTERFACE]
  wg-quic-quick check INTERFACE|CONFIG
  wg-quic-quick show [INTERFACE] [--json]
  wg-quic-quick reconcile INTERFACE CANDIDATE --expected-epoch EPOCH --expected-generation N [--request-id ID] [--json]
  wg-quic-quick reload INTERFACE [--expected-epoch EPOCH --expected-generation N] [--request-id ID] [--json]
  wg-quic-quick transaction-status INTERFACE --request-id ID [--json]
  wg-quic-quick refresh-endpoints INTERFACE [--peer PUBLIC_KEY] [--json]
  wg-quic-quick up INTERFACE
  wg-quic-quick down INTERFACE [--repair]
  wg-quic-quick version`)
	return errors.New("invalid command line")
}
