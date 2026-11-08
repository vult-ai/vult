package local

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPeerUIDIgnoresClaimedRoot(t *testing.T) {
	if socket := os.Getenv("VULT_PEER_TEST_SOCKET"); socket != "" {
		connection, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			t.Fatal("synthetic peer could not connect")
		}
		defer connection.Close()
		if _, err := io.WriteString(connection, `{"uid":0}`); err != nil {
			t.Fatal("synthetic identity claim could not be written")
		}
		return
	}
	directory, err := os.MkdirTemp("/tmp", "vult-uid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	if err = os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	address := &net.UnixAddr{Name: filepath.Join(directory, "peer.sock"), Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(address.Name, 0666); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPeerUIDIgnoresClaimedRoot$")
	command.Env = []string{"VULT_PEER_TEST_SOCKET=" + address.Name}
	expectedUID := uint32(os.Geteuid())
	if expectedUID == 0 {
		expectedUID = 65534
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: expectedUID, Gid: expectedUID}}
	}
	if err = command.Start(); err != nil {
		t.Fatal("synthetic unprivileged peer did not start", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	})
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal("synthetic peer did not connect", err)
	}
	defer connection.Close()
	if err = connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(io.LimitReader(connection, 64))
	if err != nil || string(data) != `{"uid":0}` {
		t.Fatal("synthetic claimed identity did not arrive")
	}
	peer, err := PeerIdentity(connection)
	if err != nil || peer.UID == 0 || peer.UID != expectedUID || peer.PID != uint32(command.Process.Pid) {
		t.Fatal("message identity replaced the unprivileged kernel peer", peer, err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal("synthetic peer failed", err)
	}
}
