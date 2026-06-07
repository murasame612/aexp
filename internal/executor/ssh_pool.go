package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
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
func (p *SSHPool) Get(ctx context.Context, host string, port int, user string, keyPath string) (*ssh.Client, error) {
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

	client, err = ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	p.conns[addr] = client
	return client, nil
}

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
func (p *SSHPool) Exec(ctx context.Context, host string, port int, user string, keyPath string, cmd string) (string, string, error) {
	session, err := p.newSession(host, port, user, keyPath)
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
func (p *SSHPool) ExecStream(ctx context.Context, host string, port int, user string, keyPath string, cmd string) (<-chan string, error) {
	session, err := p.newSession(host, port, user, keyPath)
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

func (p *SSHPool) newSession(host string, port int, user string, keyPath string) (*ssh.Session, error) {
	ctx := context.Background()
	client, err := p.Get(ctx, host, port, user, keyPath)
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		// Stale connection, retry once
		p.RemoveByHost(host, port)
		client, err = p.Get(ctx, host, port, user, keyPath)
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
