// Qualification only; never included in the runtime image.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func read(path string) []byte { b, e := os.ReadFile(path); must(e); return b }
func certs(b []byte) []*x509.Certificate {
	var out []*x509.Certificate
	for len(b) > 0 {
		p, rest := pem.Decode(b)
		if p == nil {
			if strings.TrimSpace(string(b)) != "" {
				panic("invalid PEM")
			}
			break
		}
		c, e := x509.ParseCertificate(p.Bytes)
		must(e)
		out = append(out, c)
		b = rest
	}
	return out
}
func checkRoots(bundle []byte, denied map[string]bool) int {
	approved := false
	count := 0
	for _, c := range certs(bundle) {
		sum := sha256.Sum256(c.Raw)
		fp := hex.EncodeToString(sum[:])
		if denied[fp] {
			panic("denied root: " + fp)
		}
		if fp == "96bcec06264976f37460779acf28c5a7cfe8a3c0aae11a8ffcee05c0bddf08c6" {
			approved = true
		}
		count++
	}
	if !approved || count < 1 {
		panic("approved ISRG Root X1 absent")
	}
	return count
}

func main() {
	bundle := read("/etc/ssl/certs/ca-certificates.crt")
	denied := map[string]bool{}
	for _, line := range strings.Split(string(read("/qualification/rejected-roots.sha256")), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			denied[line] = true
		}
	}
	if len(denied) != 3 {
		panic("invalid denylist")
	}
	count := checkRoots(bundle, denied)
	pool, e := x509.SystemCertPool()
	must(e)
	var manifest struct {
		VerificationTime int64             `json:"verification_time"`
		Files            map[string]string `json:"files"`
	}
	must(json.Unmarshal(read("/fixtures/manifest.json"), &manifest))
	if len(manifest.Files) != 13 || manifest.VerificationTime != 1792584000 {
		panic("unexpected fixture profile")
	}
	for name, expected := range manifest.Files {
		sum := sha256.Sum256(read(filepath.Join("/fixtures", name)))
		if hex.EncodeToString(sum[:]) != expected {
			panic("fixture checksum: " + name)
		}
	}
	original := x509.NewCertPool()
	if !original.AppendCertsFromPEM(read("/qualification/original-ca-certificates.crt")) {
		panic("empty original bundle")
	}
	chains := 0
	for name := range manifest.Files {
		if !strings.HasSuffix(name, ".pem") {
			continue
		}
		chain := certs(read(filepath.Join("/fixtures", name)))
		inter := x509.NewCertPool()
		for _, c := range chain[1:] {
			inter.AddCert(c)
		}
		opts := x509.VerifyOptions{Roots: original, Intermediates: inter, CurrentTime: time.Unix(manifest.VerificationTime, 0), DNSName: strings.TrimSuffix(name, ".pem")}
		_, e = chain[0].Verify(opts)
		must(e)
		opts.Roots = pool
		_, e = chain[0].Verify(opts)
		if _, ok := e.(x509.UnknownAuthorityError); !ok {
			panic(fmt.Sprintf("expected unknown authority for %s: %v", name, e))
		}
		chains++
	}
	// The public request exercises Go's effective system store. Kubernetes uses
	// its explicit CA data and is tested through the retained operator below.
	client := &http.Client{Timeout: 20 * time.Second}
	resp, e := client.Get("https://api.github.com")
	must(e)
	resp.Body.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	if r, err := client.Get(server.URL); err == nil {
		r.Body.Close()
		panic("unknown root accepted")
	}
	operatorTLS(false, false)
	operatorTLS(true, false)
	operatorTLS(true, true)
	fmt.Printf("PASS roots=%d real_chains_rejected=%d public_TLS=true operator_explicit_CA=true unknown_root_rejected=true hostname_mismatch_rejected=true\n", count, chains)
}

func operatorTLS(trusted, mismatch bool) {
	var hits atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	}))
	defer server.Close()
	ca := ""
	if trusted {
		ca = "certificate-authority-data: " + base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	}
	if mismatch {
		ca += "\n    tls-server-name: wrong.invalid"
	}
	config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\n    %s\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\ncurrent-context: test\nusers:\n- name: test\n  user:\n    token: qualification\n", server.URL, ca)
	must(os.WriteFile("/tmp/kubeconfig", []byte(config), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/operator", "--metrics-bind-address=0", "--health-probe-bind-address=0")
	cmd.Env = append(os.Environ(), "KUBECONFIG=/tmp/kubeconfig")
	output, _ := cmd.CombinedOutput()
	if trusted && !mismatch {
		if hits.Load() == 0 {
			panic("operator did not reach explicit-CA API: " + string(output))
		}
	} else {
		if hits.Load() != 0 || !strings.Contains(string(output), "x509:") {
			panic("operator did not prove certificate rejection: " + string(output))
		}
	}
}
