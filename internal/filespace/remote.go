package filespace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/store"
)

type RemoteEntry struct {
	Path       string `json:"path"`
	Name       string `json:"name,omitempty"`
	Exists     bool   `json:"exists"`
	Type       string `json:"type,omitempty"`
	Size       int64  `json:"size,omitempty"`
	ModifiedNS int64  `json:"modified_ns,omitempty"`
}

type ListResult struct {
	Entries    []RemoteEntry `json:"entries"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type HashResult struct {
	Revision       string `json:"revision"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	FileCount      int64  `json:"file_count"`
	TotalBytes     int64  `json:"total_bytes"`
}

type RemoteLocation struct {
	Resource     *store.Resource
	PhysicalPath string
	Boundary     string
}

type RemoteFS interface {
	Stat(ctx context.Context, location RemoteLocation) (RemoteEntry, error)
	List(ctx context.Context, location RemoteLocation, cursor string, limit int) (ListResult, error)
	Hash(ctx context.Context, location RemoteLocation) (HashResult, error)
}

type MetadataWriter interface {
	WriteAtomic(ctx context.Context, location RemoteLocation, data []byte, mode uint32) error
}

// RemoteRemover is intentionally separate from RemoteFS: read-only inspection
// and managed transfer code must not gain deletion capability by default.
type RemoteRemover interface {
	RemoveVerified(ctx context.Context, location RemoteLocation, expectedRevision string) error
}

type RemoteCommandRunner interface {
	Exec(ctx context.Context, resource *store.Resource, command string) (stdout, stderr string, err error)
}

type SSHPoolRunner struct {
	Pool *executor.SSHPool
}

func (r SSHPoolRunner) Exec(ctx context.Context, resource *store.Resource, command string) (string, string, error) {
	if r.Pool == nil {
		return "", "", fmt.Errorf("SSH pool is required")
	}
	return r.Pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, executor.WithResourceRemotePath(resource, command), resource.SocksProxy, resource.ProxyCommand)
}

const (
	RemoteErrorUnreachable = "unreachable"
	RemoteErrorCommand     = "command_failed"
	RemoteErrorProtocol    = "protocol_error"
)

type RemoteError struct {
	Kind   string
	Detail string
	Err    error
}

func (e *RemoteError) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Err.Error()
}

func (e *RemoteError) Unwrap() error { return e.Err }

type PythonRemoteFS struct {
	Runner RemoteCommandRunner
}

func (r PythonRemoteFS) RemoveVerified(ctx context.Context, location RemoteLocation, expectedRevision string) error {
	const script = `import hashlib,os,shutil,stat,sys
p,boundary,expected=sys.argv[1],sys.argv[2],sys.argv[3]
boundary=os.path.realpath(boundary); parent=os.path.realpath(os.path.dirname(p)); resolved=os.path.realpath(p)
if not boundary or boundary=="/" or os.path.commonpath([parent,boundary]) != boundary or resolved==boundary: raise RuntimeError("refuse removal outside or at managed boundary")
try: s=os.lstat(p)
except FileNotFoundError: raise SystemExit(0)
if stat.S_ISLNK(s.st_mode): raise RuntimeError("refuse removal of symlink payload")
if not (stat.S_ISDIR(s.st_mode) or stat.S_ISREG(s.st_mode)): raise RuntimeError("unsupported payload type")
tag=hashlib.sha256((p+"\0"+expected).encode()).hexdigest()[:16]
tomb=os.path.join(parent,".aexp-evict-"+tag)
if os.path.lexists(tomb): raise RuntimeError("managed eviction tombstone already exists")
os.rename(p,tomb)
def filehash(fp):
 h=hashlib.sha256()
 with open(fp,"rb") as f:
  for b in iter(lambda:f.read(1024*1024),b""): h.update(b)
 return h.hexdigest()
def revision(fp):
 st=os.lstat(fp)
 if stat.S_ISREG(st.st_mode): return "sha256:"+filehash(fp)
 records=[]
 for root,dirs,files in os.walk(fp,followlinks=False):
  dirs.sort(); files.sort()
  for name in list(dirs)+list(files):
   child=os.path.join(root,name); mode=os.lstat(child).st_mode
   if stat.S_ISLNK(mode): raise RuntimeError("symlink is not a valid managed payload")
  for name in dirs:
   rel=os.path.relpath(os.path.join(root,name),fp).replace(os.sep,"/"); records.append(("D",rel,"",0))
  for name in files:
   child=os.path.join(root,name); rel=os.path.relpath(child,fp).replace(os.sep,"/"); st=os.stat(child); records.append(("F",rel,filehash(child),st.st_size))
 records.sort(key=lambda x:(x[1],x[0]))
 manifest="".join(("D  "+rel+"\n") if kind=="D" else ("F "+digest+"  "+str(n)+"  "+rel+"\n") for kind,rel,digest,n in records).encode()
 return "sha256:"+hashlib.sha256(manifest).hexdigest()
try:
 actual=revision(tomb)
 if actual != expected: raise RuntimeError("eviction revision changed: expected "+expected+", actual "+actual)
 if os.path.isdir(tomb): shutil.rmtree(tomb)
 else: os.unlink(tomb)
except Exception:
 if os.path.lexists(tomb) and not os.path.lexists(p): os.rename(tomb,p)
 raise`
	return r.runJSONless(ctx, location.Resource, script, location.PhysicalPath, location.Boundary, expectedRevision)
}

func (r PythonRemoteFS) WriteAtomic(ctx context.Context, location RemoteLocation, data []byte, mode uint32) error {
	const script = `import base64,errno,os,sys,tempfile
p,boundary,payload,mode=sys.argv[1],sys.argv[2],base64.b64decode(sys.argv[3]),int(sys.argv[4])
boundary=os.path.realpath(boundary); parent=os.path.realpath(os.path.dirname(p))
if os.path.commonpath([parent,boundary]) != boundary: raise RuntimeError("path escapes managed boundary")
os.makedirs(parent,exist_ok=True)
if os.path.exists(p):
 with open(p,"rb") as f:
  if f.read()==payload: raise SystemExit(0)
 raise RuntimeError("immutable metadata destination conflict")
fd,tmp=tempfile.mkstemp(prefix=".incoming-aexp-metadata-",dir=parent)
try:
 os.write(fd,payload); os.fsync(fd); os.fchmod(fd,mode); os.close(fd); fd=-1
 try: os.link(tmp,p)
 except FileExistsError:
  with open(p,"rb") as f:
   if f.read()!=payload: raise RuntimeError("immutable metadata destination conflict")
finally:
 if fd>=0: os.close(fd)
 try: os.unlink(tmp)
 except FileNotFoundError: pass`
	return r.runJSONless(ctx, location.Resource, script, location.PhysicalPath, location.Boundary, base64.StdEncoding.EncodeToString(data), fmt.Sprintf("%d", mode))
}

func (r PythonRemoteFS) Stat(ctx context.Context, location RemoteLocation) (RemoteEntry, error) {
	const script = `import json, os, stat, sys
p,boundary=sys.argv[1],sys.argv[2]
resolved,boundary=os.path.realpath(p),os.path.realpath(boundary)
if os.path.commonpath([resolved,boundary]) != boundary: raise RuntimeError("path escapes managed boundary")
try:
 s=os.lstat(p)
except FileNotFoundError:
 print(json.dumps({"path":p,"exists":False}, separators=(",",":"))); raise SystemExit(0)
mode=s.st_mode
kind="symlink" if stat.S_ISLNK(mode) else "directory" if stat.S_ISDIR(mode) else "file" if stat.S_ISREG(mode) else "other"
print(json.dumps({"path":p,"name":os.path.basename(p),"exists":True,"type":kind,"size":s.st_size,"modified_ns":s.st_mtime_ns}, separators=(",",":")))`
	var result RemoteEntry
	if err := r.runJSON(ctx, location.Resource, script, &result, location.PhysicalPath, location.Boundary); err != nil {
		return RemoteEntry{}, err
	}
	return result, nil
}

func (r PythonRemoteFS) List(ctx context.Context, location RemoteLocation, cursor string, limit int) (ListResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const script = `import json, os, stat, sys
p,boundary,cursor,limit=sys.argv[1],sys.argv[2],sys.argv[3],int(sys.argv[4])
resolved,boundary=os.path.realpath(p),os.path.realpath(boundary)
if os.path.commonpath([resolved,boundary]) != boundary: raise RuntimeError("path escapes managed boundary")
items=[]
for e in sorted(os.scandir(p), key=lambda x:x.name):
 if e.name <= cursor: continue
 s=e.stat(follow_symlinks=False); mode=s.st_mode
 kind="symlink" if stat.S_ISLNK(mode) else "directory" if stat.S_ISDIR(mode) else "file" if stat.S_ISREG(mode) else "other"
 items.append({"path":e.path,"name":e.name,"exists":True,"type":kind,"size":s.st_size,"modified_ns":s.st_mtime_ns})
 if len(items) > limit: break
more=len(items)>limit
visible=items[:limit]
print(json.dumps({"entries":visible,"next_cursor":visible[-1]["name"] if more and visible else ""}, separators=(",",":")))`
	var result ListResult
	if err := r.runJSON(ctx, location.Resource, script, &result, location.PhysicalPath, location.Boundary, cursor, fmt.Sprintf("%d", limit)); err != nil {
		return ListResult{}, err
	}
	if result.Entries == nil {
		result.Entries = []RemoteEntry{}
	}
	return result, nil
}

func (r PythonRemoteFS) Hash(ctx context.Context, location RemoteLocation) (HashResult, error) {
	const script = `import hashlib, json, os, stat, sys
p,boundary=sys.argv[1],sys.argv[2]
resolved,boundary=os.path.realpath(p),os.path.realpath(boundary)
if os.path.commonpath([resolved,boundary]) != boundary: raise RuntimeError("path escapes managed boundary")
def filehash(fp):
 h=hashlib.sha256()
 with open(fp,"rb") as f:
  for b in iter(lambda:f.read(1024*1024),b""): h.update(b)
 return h.hexdigest()
s=os.lstat(p)
if stat.S_ISLNK(s.st_mode): raise RuntimeError("symlink is not a valid managed payload")
if stat.S_ISREG(s.st_mode):
 h=filehash(p); print(json.dumps({"revision":"sha256:"+h,"manifest_sha256":"","file_count":1,"total_bytes":s.st_size}, separators=(",",":"))); raise SystemExit(0)
if not stat.S_ISDIR(s.st_mode): raise RuntimeError("unsupported payload type")
records=[]; total=0; file_count=0
for root,dirs,files in os.walk(p,followlinks=False):
 dirs.sort(); files.sort()
 for name in list(dirs)+list(files):
  fp=os.path.join(root,name); st=os.lstat(fp)
  if stat.S_ISLNK(st.st_mode): raise RuntimeError("symlink is not a valid managed payload: "+fp)
 for name in dirs:
  fp=os.path.join(root,name); rel=os.path.relpath(fp,p).replace(os.sep,"/")
  if "\n" in rel or "\r" in rel: raise RuntimeError("newline in managed path")
  records.append(("D",rel,"",0))
 for name in files:
  fp=os.path.join(root,name); rel=os.path.relpath(fp,p).replace(os.sep,"/")
  if "\n" in rel or "\r" in rel: raise RuntimeError("newline in managed path")
  st=os.stat(fp); digest=filehash(fp); total += st.st_size; file_count += 1
  records.append(("F",rel,digest,st.st_size))
records.sort(key=lambda r:(r[1],r[0]))
manifest="".join(("D  "+rel+"\n") if kind=="D" else ("F "+digest+"  "+str(n)+"  "+rel+"\n") for kind,rel,digest,n in records).encode()
mh=hashlib.sha256(manifest).hexdigest()
print(json.dumps({"revision":"sha256:"+mh,"manifest_sha256":"sha256:"+mh,"file_count":file_count,"total_bytes":total}, separators=(",",":")))`
	var result HashResult
	if err := r.runJSON(ctx, location.Resource, script, &result, location.PhysicalPath, location.Boundary); err != nil {
		return HashResult{}, err
	}
	return result, nil
}

func (r PythonRemoteFS) runJSON(ctx context.Context, resource *store.Resource, script string, target any, args ...string) error {
	stdout, err := r.run(ctx, resource, script, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), target); err != nil {
		return &RemoteError{Kind: RemoteErrorProtocol, Detail: "decode remote filesystem response", Err: err}
	}
	return nil
}

func (r PythonRemoteFS) runJSONless(ctx context.Context, resource *store.Resource, script string, args ...string) error {
	_, err := r.run(ctx, resource, script, args...)
	return err
}

func (r PythonRemoteFS) run(ctx context.Context, resource *store.Resource, script string, args ...string) (string, error) {
	if r.Runner == nil {
		return "", &RemoteError{Kind: RemoteErrorProtocol, Detail: "remote command runner is required", Err: errors.New("missing runner")}
	}
	prelude := "import base64,sys\nsys.argv[1:]=[base64.b64decode(v).decode('utf-8') for v in sys.argv[1:]]\n" + script
	bootstrap := "import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString([]byte(prelude)) + "'))"
	parts := []string{"python3", "-c", shellQuote(bootstrap)}
	for _, arg := range args {
		if hasUnsafeText(arg) {
			return "", &RemoteError{Kind: RemoteErrorProtocol, Detail: "remote path contains unsafe characters", Err: errors.New("unsafe path")}
		}
		parts = append(parts, shellQuote(base64.StdEncoding.EncodeToString([]byte(arg))))
	}
	stdout, stderr, err := r.Runner.Exec(ctx, resource, strings.Join(parts, " "))
	if err != nil {
		kind := RemoteErrorUnreachable
		var exitStatus interface{ ExitStatus() int }
		if errors.As(err, &exitStatus) {
			kind = RemoteErrorCommand
		}
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = err.Error()
		}
		return "", &RemoteError{Kind: kind, Detail: detail, Err: err}
	}
	return stdout, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
