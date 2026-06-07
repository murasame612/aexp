package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

// SSHPool manages persistent SSH connections keyed by host:port.
type SSHPool struct {
	conns   map[string]*ssh.Client
	keys    map[string]ssh.Signer
	mu      sync.RWMutex
	timeout time.Duration
}

// NewSSHPool creates a new pool with the given default timeout.
func NewSSHPool(timeout time.Duration) *SSHPool {
	return &SSHPool{
		conns:   make(map[string]*ssh.Client),
		keys:    make(map[string]ssh.Signer),
		timeout: timeout,
	}
}

// AddKey registers an SSH private key from a file path.
func (p *SSHPool) AddKey(keyPath string) error {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("parse key %s: %w", keyPath, err)
	}
	p.mu.Lock()
	p.keys[keyPath] = signer
	p.mu.Unlock()
	return nil
}

// Get returns an existing or new SSH connection for the given host.
// socksProxy is optional, e.g. "member.aicloud.szu.edu.cn:30027" for SOCKS5.
// proxyCommand is optional, e.g. "nc -X 5 -x host:port %h %p" for SSH ProxyCommand.
// If both are set, proxyCommand takes precedence.
func (p *SSHPool) Get(ctx context.Context, host string, port int, user string, keyPath string, socksProxy string, proxyCommand string) (*ssh.Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	p.mu.RLock()
	client, ok := p.conns[addr]
	p.mu.RUnlock()

	if ok && isAlive(client) {
		return client, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if client, ok = p.conns[addr]; ok && isAlive(client) {
		return client, nil
	}

	signer, err := p.findSigner(keyPath)
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         p.timeout,
	}

	// Dial order: proxyCommand > socksProxy > direct
	if proxyCommand != "" {
		conn, dialErr := p.dialViaProxyCommand(proxyCommand, host, port)
		if dialErr != nil {
			return nil, fmt.Errorf("proxy command dial: %w", dialErr)
		}
		sshConn, chans, reqs, handshakeErr := ssh.NewClientConn(conn, addr, config)
		if handshakeErr != nil {
			conn.Close()
			return nil, fmt.Errorf("ssh handshake via proxy command: %w", handshakeErr)
		}
		client = ssh.NewClient(sshConn, chans, reqs)
	} else if socksProxy != "" {
		dialer, dialErr := proxy.SOCKS5("tcp", socksProxy, nil, proxy.Direct)
		if dialErr != nil {
			return nil, fmt.Errorf("socks5 proxy %s: %w", socksProxy, dialErr)
		}
		conn, dialErr := dialer.Dial("tcp", addr)
		if dialErr != nil {
			return nil, fmt.Errorf("socks5 dial %s via %s: %w", addr, socksProxy, dialErr)
		}
		sshConn, chans, reqs, handshakeErr := ssh.NewClientConn(conn, addr, config)
		if handshakeErr != nil {
			conn.Close()
			return nil, fmt.Errorf("ssh handshake %s via %s: %w", addr, socksProxy, handshakeErr)
		}
		client = ssh.NewClient(sshConn, chans, reqs)
	} else {
		client, err = ssh.Dial("tcp", addr, config)
		if err != nil {
			return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
		}
	}

	p.conns[addr] = client
	return client, nil
}

// dialViaProxyCommand runs an SSH ProxyCommand and returns the connection.
// Replaces %h with host and %p with port in the command template.
func (p *SSHPool) dialViaProxyCommand(tmpl string, host string, port int) (net.Conn, error) {
	cmdStr := strings.ReplaceAll(tmpl, "%h", host)
	cmdStr = strings.ReplaceAll(cmdStr, "%p", fmt.Sprintf("%d", port))

	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy cmd stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy cmd stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proxy cmd start: %w", err)
	}

	// Wrap as net.Conn
	conn := &cmdConn{
		stdin:  stdin,
		stdout: stdout,
		cmd:    cmd,
	}
	return conn, nil
}

// cmdConn wraps a command's stdin/stdout as a net.Conn.
type cmdConn struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (c *cmdConn) Read(b []byte) (int, error)  { return c.stdout.Read(b) }
func (c *cmdConn) Write(b []byte) (int, error)  { return c.stdin.Write(b) }
func (c *cmdConn) Close() error {
	c.stdin.Close()
	c.stdout.Close()
	return c.cmd.Process.Kill()
}
func (c *cmdConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *cmdConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *cmdConn) SetDeadline(t time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(t time.Time) error { return nil }

func (p *SSHPool) findSigner(keyPath string) (ssh.Signer, error) {
	if keyPath != "" {
		if s, ok := p.keys[keyPath]; ok {
			return s, nil
		}
		// Try to load it
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", keyPath, err)
		}
		s, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", keyPath, err)
		}
		p.keys[keyPath] = s
		return s, nil
	}

	// Use first available key
	for _, s := range p.keys {
		return s, nil
	}
	return nil, fmt.Errorf("no SSH key registered")
}

// Close removes and closes the connection for an address.
func (p *SSHPool) Close(addr string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.conns[addr]; ok {
		delete(p.conns, addr)
		return c.Close()
	}
	return nil
}

// CloseAll closes all connections.
func (p *SSHPool) CloseAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for addr, c := range p.conns {
		c.Close()
		delete(p.conns, addr)
	}
	return nil
}

// RemoveByHost closes the connection for a specific host:port.
func (p *SSHPool) RemoveByHost(host string, port int) {
	addr := fmt.Sprintf("%s:%d", host, port)
	p.mu.Lock()
	if c, ok := p.conns[addr]; ok {
		c.Close()
		delete(p.conns, addr)
	}
	p.mu.Unlock()
}

func isAlive(client *ssh.Client) bool {
	_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// Exec runs a command and returns stdout+stderr as strings.
func (p *SSHPool) Exec(ctx context.Context, host string, port int, user string, keyPath string, cmd string, socksProxy string, proxyCommand string) (string, string, error) {
	session, err := p.newSession(host, port, user, keyPath, socksProxy, proxyCommand)
	if err != nil {
		return "", "", err
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return "", "", err
	}

	if err := session.Start(cmd); err != nil {
		return "", "", err
	}

	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(&outBuf, stdout) }()
	go func() { defer wg.Done(); io.Copy(&errBuf, stderr) }()

	err = session.Wait()
	wg.Wait()

	return outBuf.String(), errBuf.String(), err
}

// ExecStream runs a command and streams stdout lines to a channel.
func (p *SSHPool) ExecStream(ctx context.Context, host string, port int, user string, keyPath string, cmd string, socksProxy string, proxyCommand string) (<-chan string, error) {
	session, err := p.newSession(host, port, user, keyPath, socksProxy, proxyCommand)
	if err != nil {
		return nil, err
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}

	if err := session.Start(cmd); err != nil {
		session.Close()
		return nil, err
	}

	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		defer session.Close()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case ch <- scanner.Text():
			case <-ctx.Done():
				session.Signal(ssh.SIGINT)
				return
			}
		}
		session.Wait()
	}()

	return ch, nil
}

func (p *SSHPool) newSession(host string, port int, user string, keyPath string, socksProxy string, proxyCommand string) (*ssh.Session, error) {
	ctx := context.Background()
	client, err := p.Get(ctx, host, port, user, keyPath, socksProxy, proxyCommand)
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		// Stale connection, retry once
		p.RemoveByHost(host, port)
		client, err = p.Get(ctx, host, port, user, keyPath, socksProxy, proxyCommand)
		if err != nil {
			return nil, err
		}
		session, err = client.NewSession()
		if err != nil {
			return nil, fmt.Errorf("new session: %w", err)
		}
	}
	return session, nil
}
