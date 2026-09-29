package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

func newDB(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(context.Background(), name, "ru", "be"); err != nil {
		t.Fatal(err)
	}
	return st
}

// addUnit imports one XLIFF unit into the named project.
func addUnit(t *testing.T, st *store.Store, project, source string) {
	t.Helper()
	ctx := context.Background()
	p, err := st.ProjectByName(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body := `<?xml version="1.0"?><xliff version="1.2"><file original="a" source-language="ru" target-language="be"><body>` +
		`<trans-unit id="1"><source>` + source + `</source></trans-unit></body></file></xliff>`
	if err := os.WriteFile(filepath.Join(dir, "a.xlf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(ctx, st, qa.NewRunner(p.Settings.QA), importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
}

func projectNames(t *testing.T, path string) []string {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps, err := st.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// checkSearch verifies the FTS index works in a restored database.
func checkSearch(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps, _ := st.Projects(context.Background())
	for _, p := range ps {
		if p.Name != "first" {
			continue
		}
		n, err := st.Count(context.Background(), store.Filter{ProjectID: p.ID, Query: "ПРИВЕТ"})
		if err != nil || n != 1 {
			t.Errorf("search after restore: %d %v", n, err)
		}
	}
}

func roundTrip(t *testing.T, target Target) {
	ctx := context.Background()
	st := newDB(t, "first")
	defer st.Close()
	addUnit(t, st, "first", "Привет, мир")

	r1, err := Run(ctx, st.Path, []Target{target}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Size == 0 {
		t.Fatal("empty snapshot")
	}
	time.Sleep(1100 * time.Millisecond) // names have second resolution
	if _, err := st.CreateProject(ctx, "second", "ru", "be"); err != nil {
		t.Fatal(err)
	}
	r2, err := Run(ctx, st.Path, []Target{target}, 1)
	if err != nil {
		t.Fatal(err)
	}
	names, err := List(ctx, target)
	if err != nil || len(names) != 1 || names[0] != r2.Name {
		t.Fatalf("after prune: %v %v (want only %s)", names, err, r2.Name)
	}

	dst := filepath.Join(t.TempDir(), "restored.db")
	os.WriteFile(dst, []byte("old"), 0o644)
	if err := Restore(ctx, target, "latest", dst); err != nil {
		t.Fatal(err)
	}
	if got := projectNames(t, dst); len(got) != 2 {
		t.Errorf("restored projects = %v", got)
	}
	checkSearch(t, dst)
	if b, _ := os.ReadFile(dst + ".bak"); string(b) != "old" {
		t.Errorf("previous db not kept as .bak")
	}
}

func TestLocalDir(t *testing.T) { roundTrip(t, LocalDir(t.TempDir())) }

func TestSFTP(t *testing.T) {
	addr, cfg := sftpServer(t)
	host, port, _ := net.SplitHostPort(addr)
	cfg.Host = host
	cfg.Port, _ = strconv.Atoi(port)
	target := &SFTP{cfg: cfg}
	defer target.Close()
	roundTrip(t, target)
}

// sftpServer starts an SSH server with an in-memory SFTP subsystem and
// returns its address plus a client config (key + known_hosts files).
func sftpServer(t *testing.T) (string, config.SFTP) {
	t.Helper()
	dir := t.TempDir()

	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	clientPub, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	sshClientPub, _ := ssh.NewPublicKey(clientPub)

	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "id_ed25519")
	os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)

	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if string(k.Marshal()) == string(sshClientPub.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	srvCfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	handler := sftp.InMemHandler()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(nc, srvCfg, handler)
		}
	}()

	khFile := filepath.Join(dir, "known_hosts")
	os.WriteFile(khFile, []byte(knownhosts.Line([]string{knownhosts.Normalize(ln.Addr().String())}, hostSigner.PublicKey())+"\n"), 0o600)
	return ln.Addr().String(), config.SFTP{User: "u", Key: keyFile, KnownHosts: khFile, Dir: "/backups/bellingua"}
}

func serveSSH(nc net.Conn, cfg *ssh.ServerConfig, h sftp.Handlers) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, in, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range in {
				ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv := sftp.NewRequestServer(ch, h)
					go func() { srv.Serve(); srv.Close() }()
				}
			}
		}()
	}
}
