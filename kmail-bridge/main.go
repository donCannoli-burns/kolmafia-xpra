// kmail-bridge serves KoL kmail over IMAP and SMTP, through the KoLmafia relay.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-smtp"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	password := os.Getenv("KMAIL_PASSWORD")
	if password == "" {
		log.Fatal("KMAIL_PASSWORD is required")
	}
	home, _ := os.UserHomeDir()
	dir := env("KMAIL_DIR", filepath.Join(home, ".kolmafia", "kmail-bridge"))
	interval, err := time.ParseDuration(env("KMAIL_POLL_INTERVAL", "60s"))
	if err != nil {
		log.Fatalf("KMAIL_POLL_INTERVAL: %v", err)
	}

	certFile, keyFile := os.Getenv("KMAIL_TLS_CERT"), os.Getenv("KMAIL_TLS_KEY")
	if certFile == "" {
		certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
		if err := ensureSelfSigned(certFile, keyFile, env("KMAIL_HOSTNAME", "localhost")); err != nil {
			log.Fatalf("generating certificate: %v", err)
		}
	}
	tlsConfig := &tls.Config{GetCertificate: (&certLoader{cert: certFile, key: keyFile}).get}

	bridge := NewBridge(NewKoL(env("KMAIL_RELAY_URL", "http://127.0.0.1:60080")), dir)

	imapLn, err := tls.Listen("tcp", env("KMAIL_IMAP_ADDR", ":9993"), tlsConfig)
	if err != nil {
		log.Fatal(err)
	}
	smtpLn, err := tls.Listen("tcp", env("KMAIL_SMTP_ADDR", ":9465"), tlsConfig)
	if err != nil {
		log.Fatal(err)
	}

	imapSrv := newIMAPServer(bridge, password)
	smtpSrv := newSMTPServer(bridge, password)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go bridge.Run(ctx, interval)
	go func() {
		<-ctx.Done()
		imapSrv.Close()
		smtpSrv.Close()
	}()

	log.Printf("IMAPS on %s, SMTPS on %s", imapLn.Addr(), smtpLn.Addr())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := imapSrv.Serve(imapLn); err != nil && ctx.Err() == nil {
			log.Fatalf("imap: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := smtpSrv.Serve(smtpLn); err != nil && !errors.Is(err, smtp.ErrServerClosed) {
			log.Fatalf("smtp: %v", err)
		}
	}()
	wg.Wait()
}

func newIMAPServer(bridge *Bridge, password string) *imapserver.Server {
	return imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &session{bridge: bridge, password: password}, nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1:  {},
			imap.CapUIDPlus:    {},
			imap.CapMove:       {},
			imap.CapSpecialUse: {},
			imap.CapListStatus: {},
		},
	})
}

func newSMTPServer(bridge *Bridge, password string) *smtp.Server {
	s := smtp.NewServer(&smtpBackend{bridge: bridge, password: password})
	s.Domain = domain
	s.MaxRecipients = 10
	s.MaxMessageBytes = 1 << 20
	s.ReadTimeout = 5 * time.Minute
	s.WriteTimeout = time.Minute
	return s
}

// certLoader rereads the certificate when it changes, so renewals don't need a restart.
type certLoader struct {
	cert, key string

	mu      sync.Mutex
	modTime time.Time
	loaded  *tls.Certificate
}

func (l *certLoader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	fi, err := os.Stat(l.cert)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loaded == nil || !fi.ModTime().Equal(l.modTime) {
		c, err := tls.LoadX509KeyPair(l.cert, l.key)
		if err != nil {
			return nil, err
		}
		l.loaded, l.modTime = &c, fi.ModTime()
	}
	return l.loaded, nil
}

func ensureSelfSigned(certFile, keyFile, host string) error {
	if _, err := os.Stat(certFile); err == nil {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	log.Printf("generated a self-signed certificate for %s in %s", host, certFile)
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
