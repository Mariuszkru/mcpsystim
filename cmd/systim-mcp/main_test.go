package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mkrukowski/systim-mcp/internal/auth"
	"github.com/mkrukowski/systim-mcp/internal/config"
	"github.com/mkrukowski/systim-mcp/internal/invoicing"
	"github.com/mkrukowski/systim-mcp/internal/systim"
	"github.com/mkrukowski/systim-mcp/internal/tools"
)

// idpTestowy udaje serwer autoryzacji na potrzeby testów okablowania HTTP.
type idpTestowy struct {
	srv   *httptest.Server
	klucz *rsa.PrivateKey
	kid   string
}

func nowyIdP(t *testing.T) *idpTestowy {
	t.Helper()
	klucz, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generowanie klucza: %v", err)
	}
	idp := &idpTestowy{klucz: klucz, kid: "klucz-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           idp.srv.URL,
			"jwks_uri":                         idp.srv.URL + "/jwks",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": idp.kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(klucz.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(klucz.E)).Bytes()),
		}}})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (i *idpTestowy) token(t *testing.T, zmiany map[string]any) string {
	t.Helper()
	roszczenia := map[string]any{
		"iss":   i.srv.URL,
		"sub":   "ksiegowa",
		"aud":   "systim-mcp",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"scope": "systim:faktury",
	}
	for k, v := range zmiany {
		roszczenia[k] = v
	}
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": i.kid})
	pb, _ := json.Marshal(roszczenia)
	czesc := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	suma := sha256.Sum256([]byte(czesc))
	podpis, err := rsa.SignPKCS1v15(rand.Reader, i.klucz, crypto.SHA256, suma[:])
	if err != nil {
		t.Fatalf("podpisywanie: %v", err)
	}
	return czesc + "." + base64.RawURLEncoding.EncodeToString(podpis)
}

// srodowisko buduje kompletny serwer HTTP taki, jak w produkcji.
func srodowisko(t *testing.T, idp *idpTestowy, wylaczAutoryzacje bool) (*httptest.Server, *config.Config) {
	t.Helper()

	// Atrapa API Systim — testy okablowania nie sięgają dalej niż warstwa HTTP.
	systimSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
	}))
	t.Cleanup(systimSrv.Close)

	klient, err := systim.NewClient(systim.Opcje{Login: "u", Pass: "p", BaseURL: systimSrv.URL})
	if err != nil {
		t.Fatalf("NewClient = %v", err)
	}
	stawki, err := invoicing.NoweStawkiVAT(map[string]int{"23": 1})
	if err != nil {
		t.Fatalf("NoweStawkiVAT = %v", err)
	}
	szkice, err := invoicing.NowyPodpisSzkicow([]byte(strings.Repeat("k", 48)))
	if err != nil {
		t.Fatalf("NowyPodpisSzkicow = %v", err)
	}

	cfg := &config.Config{
		Konto:        "abcd",
		IDSzablonu:   "3",
		IDNumeracji:  "5",
		KatalogPDF:   t.TempDir(),
		PublicURL:    "https://mcp.firma.pl",
		OIDCIssuer:   idp.srv.URL,
		OIDCAudience: "systim-mcp",
		OIDCScope:    "systim:faktury",
		// Scope'y ogłaszane klientowi: wymagany plus te, bez których authentik
		// nie wyda refresh tokenu.
		OIDCScopesZadane: []string{"systim:faktury", "openid", "offline_access"},
		AuthDisabled:     wylaczAutoryzacje,
		MaxPozycji:       config.DomyslnyMaxPozycji,
		MaxCialo:         config.DomyslnyMaxCialo,
	}

	mcpSrv := tools.NowySerwer(klient, stawki, szkice, cfg, nil).
		Zarejestruj(&mcp.Implementation{Name: "systim-mcp", Version: "test"}, nil)

	var walidator *auth.Walidator
	if !wylaczAutoryzacje {
		walidator = auth.NowyWalidator(
			auth.NoweZrodloKluczy(idp.srv.URL, idp.srv.Client(), nil),
			cfg.OIDCIssuer, cfg.OIDCAudience, cfg.OIDCScope)
	}

	srv := httptest.NewServer(zbudujMux(mcpSrv, cfg, walidator, nil))
	t.Cleanup(srv.Close)
	return srv, cfg
}

// zadanieMCP wysyła poprawne żądanie inicjalizujące sesję MCP.
func zadanieMCP(t *testing.T, srv *httptest.Server, naglowki map[string]string) *http.Response {
	t.Helper()
	cialo := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"test","version":"1"}}}`

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(cialo))
	if err != nil {
		t.Fatalf("NewRequest = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range naglowki {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestZadanieDoMCPBezTokenuDostaje401ZNaglowkiem(t *testing.T) {
	idp := nowyIdP(t)
	srv, cfg := srodowisko(t, idp, false)

	resp := zadanieMCP(t, srv, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("kod = %d, chcę 401", resp.StatusCode)
	}

	wa := resp.Header.Get("WWW-Authenticate")
	if wa == "" {
		t.Fatal("brak nagłówka WWW-Authenticate — Claude nie odnajdzie serwera autoryzacji")
	}
	// Bez resource_metadata klient nie wie, dokąd iść po token.
	if !strings.Contains(wa, `resource_metadata="`+cfg.URLMetadanychZasobu()+`"`) {
		t.Errorf("WWW-Authenticate = %q, chcę wskazania na %s", wa, cfg.URLMetadanychZasobu())
	}
	// Parametr scope mówi klientowi, o co ma poprosić serwer autoryzacji — a to
	// więcej niż sam scope wymagany. Bez offline_access authentik nie wyda refresh
	// tokenu i konektor rozłączy się po wygaśnięciu tokenu dostępowego.
	if !strings.Contains(wa, "systim:faktury") {
		t.Errorf("WWW-Authenticate = %q, chcę wymaganego scope systim:faktury", wa)
	}
	if !strings.Contains(wa, "offline_access") {
		t.Errorf("WWW-Authenticate = %q, chcę offline_access — bez niego authentik nie wyda refresh tokenu", wa)
	}
}

func TestZadanieDoMCPZPoprawnymTokenemPrzechodzi(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	resp := zadanieMCP(t, srv, map[string]string{
		"Authorization": "Bearer " + idp.token(t, nil),
	})
	if resp.StatusCode != http.StatusOK {
		cialo, _ := io.ReadAll(resp.Body)
		t.Fatalf("kod = %d (%s), chcę 200", resp.StatusCode, cialo)
	}
	cialo, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(cialo), "systim-mcp") {
		t.Errorf("odpowiedź nie wygląda na wynik initialize: %s", cialo)
	}
}

func TestZadanieDoMCPZeZlymTokenem(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	przypadki := []struct {
		nazwa  string
		zmiany map[string]any
		kod    int
	}{
		{"zły aud", map[string]any{"aud": "inna-aplikacja"}, http.StatusUnauthorized},
		{"po exp", map[string]any{"exp": time.Now().Add(-2 * time.Hour).Unix()}, http.StatusUnauthorized},
		{"zły iss", map[string]any{"iss": "https://obcy-idp.example"}, http.StatusUnauthorized},
		{"brak scope", map[string]any{"scope": "openid"}, http.StatusForbidden},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			resp := zadanieMCP(t, srv, map[string]string{
				"Authorization": "Bearer " + idp.token(t, p.zmiany),
			})
			if resp.StatusCode != p.kod {
				t.Errorf("kod = %d, chcę %d", resp.StatusCode, p.kod)
			}
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("brak nagłówka WWW-Authenticate")
			}
		})
	}
}

func TestTokenPodpisanyNieznanymKluczemJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	// Drugi IdP z własnym kluczem — token wygląda poprawnie, ale podpis jest obcy.
	obcy := nowyIdP(t)
	roszczenia := map[string]any{"iss": idp.srv.URL} // iss podszywa się pod właściwy
	token := obcy.token(t, roszczenia)

	resp := zadanieMCP(t, srv, map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("kod = %d, chcę 401 dla tokenu podpisanego nieznanym kluczem", resp.StatusCode)
	}
}

func TestZadanieZObcymOriginJestOdrzucane(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	// Origin sprawdzamy przed tokenem, więc nawet poprawny token nie pomaga.
	resp := zadanieMCP(t, srv, map[string]string{
		"Origin":        "https://zlosliwa-strona.example",
		"Authorization": "Bearer " + idp.token(t, nil),
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("kod = %d, chcę 403 dla obcego Origin", resp.StatusCode)
	}
}

func TestZadanieZWlasnymOriginPrzechodzi(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	resp := zadanieMCP(t, srv, map[string]string{
		"Origin":        "https://mcp.firma.pl",
		"Authorization": "Bearer " + idp.token(t, nil),
	})
	if resp.StatusCode != http.StatusOK {
		cialo, _ := io.ReadAll(resp.Body)
		t.Errorf("kod = %d (%s), chcę 200", resp.StatusCode, cialo)
	}
}

func TestHealthzDzialaBezTokenu(t *testing.T) {
	// HEALTHCHECK kontenera i sonda platformy nie mają tokenu.
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kod = %d, chcę 200", resp.StatusCode)
	}
	var v map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("dekodowanie = %v", err)
	}
	if v["status"] != "ok" {
		t.Errorf("status = %q", v["status"])
	}
}

func TestMetadaneZasobuSaPubliczne(t *testing.T) {
	idp := nowyIdP(t)
	srv, cfg := srodowisko(t, idp, false)

	// Claude pobiera ten dokument, zanim ma jakikolwiek token.
	for _, sciezka := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		resp, err := srv.Client().Get(srv.URL + sciezka)
		if err != nil {
			t.Fatalf("Get(%s) = %v", sciezka, err)
		}
		var m auth.MetadaneZasobu
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("dekodowanie %s = %v", sciezka, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: kod = %d, chcę 200", sciezka, resp.StatusCode)
		}
		if m.Resource != cfg.URLZasobu() {
			t.Errorf("%s: resource = %q, chcę %q", sciezka, m.Resource, cfg.URLZasobu())
		}
		if len(m.AuthorizationServers) != 1 || m.AuthorizationServers[0] != cfg.OIDCIssuer {
			t.Errorf("%s: authorization_servers = %v", sciezka, m.AuthorizationServers)
		}
		if len(m.ScopesSupported) == 0 || m.ScopesSupported[0] != "systim:faktury" {
			t.Errorf("%s: scopes_supported = %v, chcę na pierwszym miejscu systim:faktury", sciezka, m.ScopesSupported)
		}
		// Bez offline_access authentik nie wyda refresh tokenu i konektor
		// rozłączy się, gdy wygaśnie token dostępowy.
		if !slices.Contains(m.ScopesSupported, "offline_access") {
			t.Errorf("%s: scopes_supported = %v, brakuje offline_access", sciezka, m.ScopesSupported)
		}
	}
}

func TestWylaczonaAutoryzacjaPrzepuszczaZadania(t *testing.T) {
	// Tryb SYSTIM_AUTH_DISABLED — wyłącznie do testów lokalnych.
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, true)

	resp := zadanieMCP(t, srv, nil)
	if resp.StatusCode != http.StatusOK {
		cialo, _ := io.ReadAll(resp.Body)
		t.Errorf("kod = %d (%s), chcę 200 przy wyłączonej autoryzacji", resp.StatusCode, cialo)
	}
}

func TestWylaczonaAutoryzacjaNadalSprawdzaOrigin(t *testing.T) {
	// Ochrona przed DNS rebinding nie zależy od tego, czy tokeny są walidowane.
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, true)

	resp := zadanieMCP(t, srv, map[string]string{"Origin": "https://zlosliwa-strona.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("kod = %d, chcę 403", resp.StatusCode)
	}
}

func TestZbytDuzeCialoZadaniaJestOdrzucane(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, true)

	// Ciało znacznie ponad limit MaxCialo.
	wielkie := strings.Repeat("x", int(config.DomyslnyMaxCialo)+1024)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"x":"`+wielkie+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		// Serwer może zerwać połączenie przy przekroczeniu limitu — to też jest
		// poprawne odrzucenie.
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("kod = 200; żądanie ponad limit %d bajtów zostało przyjęte", config.DomyslnyMaxCialo)
	}
}

func TestNieznanaSciezkaDaje404(t *testing.T) {
	idp := nowyIdP(t)
	srv, _ := srodowisko(t, idp, false)

	// Stary transport SSE nie jest wspierany i nie ma być wystawiony.
	for _, sciezka := range []string{"/sse", "/messages", "/"} {
		resp, err := srv.Client().Get(srv.URL + sciezka)
		if err != nil {
			t.Fatalf("Get(%s) = %v", sciezka, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: kod = %d, chcę 404", sciezka, resp.StatusCode)
		}
	}
}

func TestZbudujMuxNieWymagaWalidatoraDoStartu(t *testing.T) {
	// Sanity check: mux musi się złożyć, zanim IdP wstanie.
	cfg := &config.Config{PublicURL: "https://mcp.firma.pl", MaxCialo: config.DomyslnyMaxCialo}
	h := zbudujMux(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), cfg, nil, nil)
	if h == nil {
		t.Fatal("zbudujMux = nil")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, chcę 200", rec.Code)
	}
}
