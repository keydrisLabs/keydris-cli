package deviceservice

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type SocketClient struct {
	Path    string
	Timeout time.Duration
}

var _ Client = SocketClient{}

func (c SocketClient) Health(ctx context.Context) (Health, error) {
	reply, err := c.exchange(ctx, request{Version: ProtocolVersion, Action: actionHealth})
	if err != nil {
		return Health{}, err
	}
	if reply.Health == nil {
		return Health{}, fmt.Errorf("device service returned no health")
	}
	return *reply.Health, nil
}

func (c SocketClient) Identity(ctx context.Context) (*Identity, error) {
	reply, err := c.exchange(ctx, request{Version: ProtocolVersion, Action: actionIdentity})
	if err != nil {
		return nil, err
	}
	if reply.Identity == nil {
		return nil, fmt.Errorf("device service returned no identity")
	}
	return reply.Identity, nil
}

func (c SocketClient) CreateCSR(ctx context.Context, value CSRRequest) (CSR, error) {
	reply, err := c.exchange(ctx, request{Version: ProtocolVersion, Action: actionCreateCSR, CSR: &value})
	if err != nil {
		return CSR{}, err
	}
	if reply.CSR == nil {
		return CSR{}, fmt.Errorf("device service returned no CSR")
	}
	return *reply.CSR, nil
}

func (c SocketClient) Sign(ctx context.Context, value SignRequest) (Signature, error) {
	reply, err := c.exchange(ctx, request{Version: ProtocolVersion, Action: actionSign, Sign: &value})
	if err != nil {
		return Signature{}, err
	}
	if reply.Signature == nil {
		return Signature{}, fmt.Errorf("device service returned no signature")
	}
	return *reply.Signature, nil
}

func (c SocketClient) InstallIdentity(ctx context.Context, value Identity) error {
	_, err := c.exchange(ctx, request{Version: ProtocolVersion, Action: actionInstall, Identity: &value})
	return err
}

func (c SocketClient) Renew(ctx context.Context) (*Identity, error) {
	reply, err := c.exchange(ctx, request{Version: ProtocolVersion, Action: actionRenew})
	if err != nil {
		return nil, err
	}
	if reply.Identity == nil {
		return nil, fmt.Errorf("device service returned no identity")
	}
	return reply.Identity, nil
}

func (c SocketClient) exchange(ctx context.Context, message request) (*response, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, fmt.Errorf("connect to device service: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(connection).Encode(message); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	if !scanner.Scan() {
		return nil, fmt.Errorf("device service returned no response")
	}
	var reply response
	if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
		return nil, fmt.Errorf("decode device service response: %w", err)
	}
	if !reply.OK {
		if reply.Error == "" {
			reply.Error = "request rejected"
		}
		return nil, fmt.Errorf("device service: %s", reply.Error)
	}
	return &reply, nil
}
