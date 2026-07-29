package transfer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
)

type TransferCommandRunner interface {
	Exec(ctx context.Context, resource *store.Resource, command string) (stdout, stderr string, err error)
	Stream(ctx context.Context, resource *store.Resource, command string, onRecord func(string) error) (stderr string, err error)
}

// SSHPoolTransferRunner executes control commands on the selected initiator.
// A synthetic local resource is the only path that executes on the Mac.
type SSHPoolTransferRunner struct {
	Pool *executor.SSHPool
}

func (r SSHPoolTransferRunner) Exec(ctx context.Context, resource *store.Resource, command string) (string, string, error) {
	if isLocalResource(resource) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	if r.Pool == nil || resource == nil {
		return "", "", fmt.Errorf("SSH command runner is unavailable")
	}
	return r.Pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, executor.WithResourceRemotePath(resource, command), resource.SocksProxy, resource.ProxyCommand)
}

func (r SSHPoolTransferRunner) Stream(ctx context.Context, resource *store.Resource, command string, onRecord func(string) error) (string, error) {
	if isLocalResource(resource) {
		return streamLocalCommand(ctx, command, onRecord)
	}
	if r.Pool == nil || resource == nil {
		return "", fmt.Errorf("SSH command runner is unavailable")
	}
	return r.Pool.ExecStreamLines(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, executor.WithResourceRemotePath(resource, command), resource.SocksProxy, resource.ProxyCommand, onRecord)
}

type RsyncTransport struct {
	Store  store.Store
	FS     filespace.RemoteFS
	Runner TransferCommandRunner
}

type rsyncCapabilities struct {
	OS           string
	Lock         string
	Rsync        bool
	AppendVerify bool
	ProtectArgs  bool
	Progress2    bool
}

func NewRsyncTransport(db store.Store, fs filespace.RemoteFS, runner TransferCommandRunner) *RsyncTransport {
	return &RsyncTransport{Store: db, FS: fs, Runner: runner}
}

func (t *RsyncTransport) Copy(ctx context.Context, request CopyRequest, report func(Progress) error) error {
	initiator, err := t.resource(ctx, request.Route.CommandResourceID)
	if err != nil {
		return operationError("initiator_unavailable", true, err)
	}
	sourceResource, err := t.endpointResource(ctx, request.Plan.Source)
	if err != nil {
		return operationError("source_resource_unavailable", true, err)
	}
	destinationResource, err := t.endpointResource(ctx, request.Plan.Destination)
	if err != nil {
		return operationError("destination_resource_unavailable", true, err)
	}
	if t.FS == nil || t.Runner == nil {
		return operationError("transport_unavailable", false, fmt.Errorf("rsync transport dependencies are unavailable"))
	}
	sourceInfo, err := t.stat(ctx, filespace.RemoteLocation{Resource: sourceResource, PhysicalPath: request.Plan.Source.PhysicalPath, Boundary: request.Plan.Source.Boundary})
	if err != nil {
		return operationError("source_stat_failed", true, err)
	}
	if !sourceInfo.Exists || (sourceInfo.Type != "file" && sourceInfo.Type != "directory") {
		return operationError("source_not_copyable", false, fmt.Errorf("source is not a regular file or directory"))
	}
	if len(request.Plan.Selection) > 0 && sourceInfo.Type != "directory" {
		return operationError("selection_source_not_directory", false, fmt.Errorf("selection transfer requires a directory source"))
	}
	capabilities, err := t.detectCapabilities(ctx, initiator)
	if err != nil {
		return err
	}
	command, err := t.copyCommand(initiator, sourceResource, destinationResource, request, sourceInfo.Type, capabilities)
	if err != nil {
		return operationError("route_not_executable", false, err)
	}
	progress := Progress{}
	stderr, err := t.Runner.Stream(ctx, initiator, command, func(record string) error {
		next, ok := parseRsyncProgress(record, progress)
		if !ok {
			return nil
		}
		if next.BytesDone < progress.BytesDone || next.FilesDone < progress.FilesDone {
			return nil
		}
		progress = next
		if report != nil {
			return report(progress)
		}
		return nil
	})
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = err.Error()
		}
		return operationError("rsync_failed", true, errors.New(detail))
	}
	if request.Plan.TotalBytes > progress.BytesDone {
		progress.BytesDone = request.Plan.TotalBytes
	}
	if request.Plan.FileCount > progress.FilesDone {
		progress.FilesDone = request.Plan.FileCount
	}
	if report != nil {
		return report(progress)
	}
	return nil
}

func (t *RsyncTransport) Verify(ctx context.Context, request CopyRequest) (VerifyResult, error) {
	resource, err := t.endpointResource(ctx, request.Plan.Destination)
	if err != nil {
		return VerifyResult{}, operationError("destination_resource_unavailable", true, err)
	}
	result, err := t.hash(ctx, filespace.RemoteLocation{Resource: resource, PhysicalPath: request.StagingPath, Boundary: request.Plan.Destination.Boundary})
	if err != nil {
		return VerifyResult{}, operationError("destination_hash_failed", true, err)
	}
	return VerifyResult{Revision: result.Revision, TotalBytes: result.TotalBytes, FileCount: result.FileCount}, nil
}

func (t *RsyncTransport) Promote(ctx context.Context, request CopyRequest) error {
	resource, err := t.endpointResource(ctx, request.Plan.Destination)
	if err != nil {
		return operationError("destination_resource_unavailable", true, err)
	}
	finalLocation := filespace.RemoteLocation{Resource: resource, PhysicalPath: request.Plan.Destination.PhysicalPath, Boundary: request.Plan.Destination.Boundary}
	entry, err := t.stat(ctx, finalLocation)
	if err != nil {
		return operationError("destination_stat_failed", true, err)
	}
	if entry.Exists {
		return t.acceptExistingDestination(ctx, finalLocation, request.Plan.Source.Revision)
	}
	stdout, stderr, err := t.Runner.Exec(ctx, resource, atomicPromoteCommand(request.StagingPath, request.Plan.Destination.PhysicalPath, request.Plan.Destination.Boundary))
	if err == nil {
		return nil
	}
	if strings.Contains(stdout, "AEXP_DESTINATION_EXISTS") {
		entry, statErr := t.stat(ctx, finalLocation)
		if statErr == nil && entry.Exists {
			return t.acceptExistingDestination(ctx, finalLocation, request.Plan.Source.Revision)
		}
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	return operationError("promotion_failed", true, errors.New(detail))
}

func (t *RsyncTransport) acceptExistingDestination(ctx context.Context, location filespace.RemoteLocation, expected string) error {
	result, err := t.hash(ctx, location)
	if err != nil {
		return operationError("destination_hash_failed", true, err)
	}
	if result.Revision == expected {
		return nil
	}
	return &OperationError{Code: "destination_conflict", Conflict: true, Err: fmt.Errorf("destination revision %s differs from source %s", result.Revision, expected)}
}

func (t *RsyncTransport) detectCapabilities(ctx context.Context, initiator *store.Resource) (rsyncCapabilities, error) {
	const probe = `set +e
printf 'AEXP_OS='; uname -s 2>/dev/null || printf unknown; printf '\n'
if command -v flock >/dev/null 2>&1; then printf 'AEXP_LOCK=flock\n'; elif command -v lockf >/dev/null 2>&1; then printf 'AEXP_LOCK=lockf\n'; else printf 'AEXP_LOCK=none\n'; fi
if command -v rsync >/dev/null 2>&1; then printf 'AEXP_RSYNC=1\n'; else printf 'AEXP_RSYNC=0\n'; fi
if rsync --append-verify --version >/dev/null 2>&1; then printf 'AEXP_RSYNC_APPEND_VERIFY=1\n'; else printf 'AEXP_RSYNC_APPEND_VERIFY=0\n'; fi
if rsync --protect-args --version >/dev/null 2>&1; then printf 'AEXP_RSYNC_PROTECT_ARGS=1\n'; else printf 'AEXP_RSYNC_PROTECT_ARGS=0\n'; fi
if rsync --info=progress2 --version >/dev/null 2>&1; then printf 'AEXP_RSYNC_PROGRESS2=1\n'; else printf 'AEXP_RSYNC_PROGRESS2=0\n'; fi`
	stdout, stderr, err := t.Runner.Exec(ctx, initiator, probe)
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = err.Error()
		}
		return rsyncCapabilities{}, operationError("transport_capability_probe_failed", true, errors.New(detail))
	}
	values := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	capabilities := rsyncCapabilities{
		OS:           values["AEXP_OS"],
		Lock:         values["AEXP_LOCK"],
		Rsync:        values["AEXP_RSYNC"] == "1",
		AppendVerify: values["AEXP_RSYNC_APPEND_VERIFY"] == "1",
		ProtectArgs:  values["AEXP_RSYNC_PROTECT_ARGS"] == "1",
		Progress2:    values["AEXP_RSYNC_PROGRESS2"] == "1",
	}
	if !capabilities.Rsync {
		return rsyncCapabilities{}, operationError("transport_rsync_unavailable", false, fmt.Errorf("rsync is unavailable on transfer initiator %s", initiator.Name))
	}
	if capabilities.Lock != "flock" && capabilities.Lock != "lockf" {
		return rsyncCapabilities{}, operationError("transport_lock_unavailable", false, fmt.Errorf("neither flock nor lockf is available on transfer initiator %s", initiator.Name))
	}
	return capabilities, nil
}

func (t *RsyncTransport) copyCommand(initiator, source, destination *store.Resource, request CopyRequest, sourceType string, capabilities rsyncCapabilities) (string, error) {
	if !sameResource(initiator, source) && !sameResource(initiator, destination) {
		return "", fmt.Errorf("initiator is neither source nor destination")
	}
	sourcePath := request.Plan.Source.PhysicalPath
	if sourceType == "directory" || len(request.Plan.Selection) > 0 {
		sourcePath = strings.TrimSuffix(sourcePath, "/") + "/"
	}
	sourceArg, sourceRemote := endpointRsyncArg(initiator, source, sourcePath)
	destinationArg, destinationRemote := endpointRsyncArg(initiator, destination, request.StagingPath)
	if sourceRemote && destinationRemote {
		return "", fmt.Errorf("rsync cannot use two remote endpoints")
	}
	peer := source
	if destinationRemote {
		peer = destination
	}
	lockedPrelude := "set -eu; "
	if request.Route.Initiator == "nas" {
		lockedPrelude += "identity=\"$HOME/" + store.NASInitiatedIdentity + "\"; "
	}
	selectionOption := ""
	if len(request.Plan.Selection) > 0 {
		encoded := base64.StdEncoding.EncodeToString([]byte(selectionFileList(request.Plan.Selection)))
		lockedPrelude += "files_from=$(mktemp); trap 'rm -f \"$files_from\"' EXIT; printf %s " + transferShellQuote(encoded) + " | python3 -c " + transferShellQuote("import base64,sys;sys.stdout.buffer.write(base64.b64decode(sys.stdin.buffer.read()))") + " > \"$files_from\"; "
		selectionOption = " --files-from=\"$files_from\""
	}
	rsh := ""
	if sourceRemote || destinationRemote {
		rshCommand := t.remoteShell(initiator, peer, request.Route.Initiator)
		rsh = " -e \"" + strings.ReplaceAll(rshCommand, "\"", "\\\"") + "\""
	}
	if !capabilities.ProtectArgs && (strings.ContainsAny(sourceArg, " \t\n") || strings.ContainsAny(destinationArg, " \t\n")) {
		return "", fmt.Errorf("rsync on transfer initiator %s lacks --protect-args and cannot safely transfer paths containing whitespace", initiator.Name)
	}
	stagingPrep := "mkdir -p " + transferShellQuote(path.Dir(request.StagingPath))
	if sourceType == "directory" {
		// NAS exports can expose ACL-backed directories with a synthetic 000
		// mode. Archive-mode rsync otherwise applies that mode to the staging
		// directory before copying its children, making the directory unusable
		// on a regular Linux destination. Reset an existing staging directory as
		// well so a failed durable job can be retried safely.
		stagingPrep += " " + transferShellQuote(request.StagingPath) + "; chmod 700 " + transferShellQuote(request.StagingPath)
	}
	if sameResource(initiator, destination) {
		lockedPrelude += stagingPrep + "; "
	} else {
		remoteHost := destination.User + "@" + destination.Host
		lockedPrelude += t.remoteShell(initiator, destination, request.Route.Initiator) + " " + transferShellQuote(remoteHost) + " " + transferShellQuote(stagingPrep) + "; "
	}
	// Normalize ownership and ACL-derived permission bits at the destination.
	// File executability is retained when present, while directories always
	// remain traversable by the destination account.
	rsyncOptions := []string{"-a", "--no-owner", "--no-group", "--chmod=Du+rwx,Dgo-rwx,Fu+rw,Fgo-rwx", "--partial"}
	if capabilities.AppendVerify {
		rsyncOptions = append(rsyncOptions, "--append-verify")
	}
	if capabilities.ProtectArgs {
		rsyncOptions = append(rsyncOptions, "--protect-args")
	}
	if capabilities.Progress2 {
		rsyncOptions = append(rsyncOptions, "--info=progress2")
	}
	rsyncCommand := "rsync " + strings.Join(rsyncOptions, " ") + " --out-format=" + transferShellQuote("AEXP_FILE\t%i\t%l\t%n") + selectionOption + rsh + " -- " + transferShellQuote(sourceArg) + " " + transferShellQuote(destinationArg)
	// The SSH client can disappear while the initiator-side rsync survives.
	// Serialize every replay of the same durable job so recovery waits for an
	// orphaned copy instead of concurrently writing the shared staging path.
	lockPath := path.Join("/tmp", "aexp-transfer-"+request.TransferID+".lock")
	switch capabilities.Lock {
	case "flock":
		return "set -eu; flock -x " + transferShellQuote(lockPath) + " -c " + transferShellQuote(lockedPrelude+rsyncCommand), nil
	case "lockf":
		return "set -eu; lockf -k " + transferShellQuote(lockPath) + " sh -c " + transferShellQuote(lockedPrelude+rsyncCommand), nil
	default:
		return "", fmt.Errorf("unsupported transfer lock primitive %q", capabilities.Lock)
	}
}

func (t *RsyncTransport) remoteShell(initiator, peer *store.Resource, route string) string {
	parts := []string{"ssh", "-p", strconv.Itoa(peer.Port), "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=accept-new"}
	if route == "nas" {
		parts = append(parts, "-i", "$identity", "-o", "IdentitiesOnly=yes")
	} else if isLocalResource(initiator) && peer.AuthRef != "" {
		parts = append(parts, "-i", peer.AuthRef, "-o", "IdentitiesOnly=yes")
	}
	if isLocalResource(initiator) && peer.ProxyCommand != "" {
		parts = append(parts, "-o", "ProxyCommand="+peer.ProxyCommand)
	}
	return strings.Join(parts, " ")
}

func endpointRsyncArg(initiator, endpoint *store.Resource, physicalPath string) (string, bool) {
	if sameResource(initiator, endpoint) {
		return physicalPath, false
	}
	host := endpoint.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return endpoint.User + "@" + host + ":" + physicalPath, true
}

func (t *RsyncTransport) resource(ctx context.Context, id string) (*store.Resource, error) {
	if id == "local" {
		return &store.Resource{ID: "local", Name: "Mac", Type: "local"}, nil
	}
	if id == "" {
		return nil, fmt.Errorf("command resource is missing")
	}
	resource, err := t.Store.GetResource(ctx, id)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", id)
		}
		return nil, err
	}
	return resource, nil
}

func (t *RsyncTransport) endpointResource(ctx context.Context, endpoint Endpoint) (*store.Resource, error) {
	if endpoint.ResourceID == "local" || endpoint.Scheme == "local" {
		return &store.Resource{ID: "local", Name: "Mac", Type: "local"}, nil
	}
	return t.resource(ctx, endpoint.ResourceID)
}

func (t *RsyncTransport) stat(ctx context.Context, location filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	if isLocalResource(location.Resource) {
		return filespace.StatLocalPath(location.PhysicalPath, location.Boundary)
	}
	if t.FS == nil {
		return filespace.RemoteEntry{}, fmt.Errorf("remote filesystem is unavailable")
	}
	return t.FS.Stat(ctx, location)
}

func (t *RsyncTransport) hash(ctx context.Context, location filespace.RemoteLocation) (filespace.HashResult, error) {
	if !isLocalResource(location.Resource) {
		if t.FS == nil {
			return filespace.HashResult{}, fmt.Errorf("remote filesystem is unavailable")
		}
		return t.FS.Hash(ctx, location)
	}
	return filespace.HashLocalPath(location.PhysicalPath, location.Boundary)
}

var rsyncProgressPattern = regexp.MustCompile(`^\s*([0-9,]+)\s+[0-9]+%.*\(xfr#([0-9]+)`)

func parseRsyncProgress(record string, current Progress) (Progress, bool) {
	if match := rsyncProgressPattern.FindStringSubmatch(strings.TrimSpace(record)); len(match) == 3 {
		bytesDone, err1 := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
		filesDone, err2 := strconv.ParseInt(match[2], 10, 64)
		if err1 == nil && err2 == nil {
			return Progress{BytesDone: bytesDone, FilesDone: filesDone}, true
		}
	}
	if strings.HasPrefix(record, "AEXP_FILE\t") {
		parts := strings.SplitN(record, "\t", 4)
		if len(parts) == 4 && strings.Contains(parts[1], "f") {
			size, err := strconv.ParseInt(parts[2], 10, 64)
			if err == nil {
				return Progress{BytesDone: current.BytesDone + size, FilesDone: current.FilesDone + 1}, true
			}
		}
	}
	return current, false
}

func atomicPromoteCommand(staging, final, boundary string) string {
	const script = `import os,sys
s,d,b=sys.argv[1],sys.argv[2],sys.argv[3]
b=os.path.realpath(b)
for p in (s,d):
 if os.path.commonpath([os.path.realpath(p),b]) != b: raise RuntimeError("path escapes managed boundary")
if os.path.lexists(d): print("AEXP_DESTINATION_EXISTS"); raise SystemExit(17)
if not os.path.lexists(s): raise RuntimeError("transfer staging path is missing")
os.makedirs(os.path.dirname(d),exist_ok=True)
os.rename(s,d)
print("AEXP_PROMOTED")`
	bootstrap := "import base64,sys;sys.argv[1:]=[base64.b64decode(v).decode('utf-8') for v in sys.argv[1:]];exec(base64.b64decode('" + base64.StdEncoding.EncodeToString([]byte(script)) + "'))"
	return "python3 -c " + transferShellQuote(bootstrap) + " " + transferShellQuote(base64.StdEncoding.EncodeToString([]byte(staging))) + " " + transferShellQuote(base64.StdEncoding.EncodeToString([]byte(final))) + " " + transferShellQuote(base64.StdEncoding.EncodeToString([]byte(boundary)))
}

func streamLocalCommand(ctx context.Context, command string, onRecord func(string) error) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var callbackErr error
	reader := bufio.NewReader(stdout)
	for {
		record, readErr := readProgressRecord(reader)
		if record != "" && onRecord != nil && callbackErr == nil {
			callbackErr = onRecord(record)
		}
		if readErr != nil {
			if readErr != io.EOF && callbackErr == nil {
				callbackErr = readErr
			}
			break
		}
	}
	waitErr := cmd.Wait()
	if callbackErr != nil {
		return stderr.String(), callbackErr
	}
	return stderr.String(), waitErr
}

func readProgressRecord(reader *bufio.Reader) (string, error) {
	var value strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return value.String(), err
		}
		if b == '\n' || b == '\r' {
			if value.Len() == 0 {
				continue
			}
			return value.String(), nil
		}
		value.WriteByte(b)
	}
}

func sameResource(a, b *store.Resource) bool {
	return a != nil && b != nil && a.ID == b.ID
}

func isLocalResource(resource *store.Resource) bool {
	return resource != nil && (resource.ID == "local" || resource.Type == "local")
}

func transferShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func operationError(code string, retryable bool, err error) error {
	return &OperationError{Code: code, Retryable: retryable, Err: err}
}
