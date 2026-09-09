package computeruse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func SocketPath(uid string) (string, error) {
	if uid == "" || len(uid) > 256 {
		return "", ErrRejected
	}
	hash := sha256.Sum256([]byte(uid))
	return filepath.Join("/tmp", "gratefulagents-desktop-"+hex.EncodeToString(hash[:16]), "relay.sock"), nil
}

func Listen(ctx context.Context, b *Broker, uid string) (io.Closer, error) {
	path, err := SocketPath(uid)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, ErrRejected
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrRejected
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, ErrRejected
		}
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, ErrRejected
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, ErrRejected
		}
		if os.Remove(path) != nil {
			return nil, ErrRejected
		}
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, ErrRejected
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, ErrRejected
	}
	go func() {
		select {
		case <-ctx.Done():
			l.Close()
			b.Close()
		case <-b.done:
			l.Close()
		}
	}()
	go func() {
		slots := make(chan struct{}, 4)
		for {
			c, err := l.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				c.Close()
				continue
			}
			go func() {
				defer func() { c.Close(); <-slots }()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				var exchange Exchange
				response := Response{Reason: "computer use request rejected"}
				if Decode(c, &exchange) == nil {
					if result, err := b.Exchange(exchange); err == nil {
						response = result
					}
				}
				_ = json.NewEncoder(c).Encode(response)
			}()
		}
	}()
	return l, nil
}

func Bridge(ctx context.Context, uid string, stdin io.Reader, stdout io.Writer) error {
	var exchange Exchange
	if Decode(stdin, &exchange) != nil || exchange.Validate() != nil {
		return ErrRejected
	}
	input, err := json.Marshal(exchange)
	if err != nil || len(input) > MaxWire {
		return ErrRejected
	}
	path, err := SocketPath(uid)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return ErrRejected
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := conn.Write(input); err != nil {
		return ErrRejected
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return ErrRejected
	}
	var response Response
	if Decode(conn, &response) != nil || response.Validate() != nil {
		return ErrRejected
	}
	output, err := json.Marshal(response)
	if err != nil || len(output) > MaxWire {
		return ErrRejected
	}
	if _, err := stdout.Write(output); err != nil {
		return ErrRejected
	}
	return nil
}
