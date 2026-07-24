package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// idpTestowy udaje serwer autoryzacji: hostuje dokument discovery i JWKS,
// oraz umie podpisać token swoim kluczem.
type idpTestowy struct {
	srv      *httptest.Server
	klucz    *rsa.PrivateKey
	kid      string
	pobrania *atomic.Int32
}

func nowyIdP(t *testing.T) *idpTestowy {
	t.Helper()
	klucz, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generowanie klucza: %v", err)
	}
	idp := &idpTestowy{klucz: klucz, kid: "klucz-1", pobrania: &atomic.Int32{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           idp.srv.URL,
			"jwks_uri":                         idp.srv.URL + "/jwks",
			"authorization_endpoint":           idp.srv.URL + "/auth",
			"token_endpoint":                   idp.srv.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		idp.pobrania.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{idp.jwk()}})
	})

	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (i *idpTestowy) jwk() map[string]any {
	return map[string]any{
		"kty": "RSA",
		"kid": i.kid,
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(i.klucz.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.klucz.E)).Bytes()),
	}
}

// podpisz tworzy token RS256 z podanymi roszczeniami.
func (i *idpTestowy) podpisz(t *testing.T, roszczenia map[string]any) string {
	t.Helper()
	return podpiszRS256(t, i.klucz, i.kid, roszczenia)
}

func podpiszRS256(t *testing.T, klucz *rsa.PrivateKey, kid string, roszczenia map[string]any) string {
	t.Helper()
	naglowek := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(naglowek)
	pb, _ := json.Marshal(roszczenia)

	czesc := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	suma := sha256.Sum256([]byte(czesc))
	podpis, err := rsa.SignPKCS1v15(rand.Reader, klucz, crypto.SHA256, suma[:])
	if err != nil {
		t.Fatalf("podpisywanie: %v", err)
	}
	return czesc + "." + base64.RawURLEncoding.EncodeToString(podpis)
}

// walidatorDo buduje walidator wskazujący na testowy IdP.
func walidatorDo(t *testing.T, idp *idpTestowy, audience, scope string) *Walidator {
	t.Helper()
	z := NoweZrodloKluczy(idp.srv.URL, idp.srv.Client(), nil)
	return NowyWalidator(z, idp.srv.URL, audience, scope)
}

func roszczeniaPoprawne(idp *idpTestowy) map[string]any {
	return map[string]any{
		"iss":   idp.srv.URL,
		"sub":   "uzytkownik-1",
		"aud":   "systim-mcp",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"scope": "openid systim:faktury",
	}
}

func TestTokenPoprawnyPrzechodzi(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, roszczeniaPoprawne(idp)))
	if err != nil {
		t.Fatalf("Zweryfikuj = %v, chcę sukcesu", err)
	}
	if r.Subject != "uzytkownik-1" {
		t.Errorf("sub = %q", r.Subject)
	}
	if !r.MaScope("systim:faktury") {
		t.Errorf("scope = %v, chcę systim:faktury", r.Uprawnienia())
	}
}

func TestTokenZBlednymAudienceJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r := roszczeniaPoprawne(idp)
	r["aud"] = "inna-aplikacja"

	_, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, r))
	if !errors.Is(err, ErrZlaAudience) {
		t.Fatalf("err = %v, chcę ErrZlaAudience", err)
	}
}

func TestTokenPoExpJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r := roszczeniaPoprawne(idp)
	// Z zapasem poza tolerancję zegara.
	r["exp"] = time.Now().Add(-10 * time.Minute).Unix()

	_, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, r))
	if !errors.Is(err, ErrTokenWygasl) {
		t.Fatalf("err = %v, chcę ErrTokenWygasl", err)
	}
}

func TestTokenPodpisanyNieznanymKluczemJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	// Klucz atakującego, ale kid ten sam co u prawdziwego IdP.
	obcy, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generowanie klucza: %v", err)
	}
	token := podpiszRS256(t, obcy, idp.kid, roszczeniaPoprawne(idp))

	_, err = w.Zweryfikuj(context.Background(), token)
	if !errors.Is(err, ErrTokenNiepoprawny) {
		t.Fatalf("err = %v, chcę ErrTokenNiepoprawny", err)
	}
}

func TestTokenZNieznanymKidJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	token := podpiszRS256(t, idp.klucz, "kid-ktorego-nie-ma", roszczeniaPoprawne(idp))
	if _, err := w.Zweryfikuj(context.Background(), token); !errors.Is(err, ErrTokenNiepoprawny) {
		t.Fatalf("err = %v, chcę ErrTokenNiepoprawny", err)
	}
}

func TestTokenZBlednymIssuerJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r := roszczeniaPoprawne(idp)
	r["iss"] = "https://zly-idp.example.com"

	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, r)); !errors.Is(err, ErrZlyIssuer) {
		t.Fatalf("err = %v, chcę ErrZlyIssuer", err)
	}
}

func TestTokenBezWymaganegoScopeJestOdrzucany(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r := roszczeniaPoprawne(idp)
	r["scope"] = "openid profile"

	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, r)); !errors.Is(err, ErrBrakScope) {
		t.Fatalf("err = %v, chcę ErrBrakScope", err)
	}
}

func TestTokenZAlgNoneJestOdrzucany(t *testing.T) {
	// Klasyczny atak na weryfikację JWT: token bez podpisu z alg="none".
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	hb, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT", "kid": idp.kid})
	pb, _ := json.Marshal(roszczeniaPoprawne(idp))
	token := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb) + "."

	if _, err := w.Zweryfikuj(context.Background(), token); !errors.Is(err, ErrTokenNiepoprawny) {
		t.Fatalf("err = %v, chcę odrzucenia tokenu z alg=none", err)
	}
}

func TestTokenZAlgHS256JestOdrzucany(t *testing.T) {
	// Drugi klasyczny atak: podmiana algorytmu na HMAC, żeby klucz publiczny RSA
	// posłużył jako sekret HMAC.
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	hb, _ := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT", "kid": idp.kid})
	pb, _ := json.Marshal(roszczeniaPoprawne(idp))
	token := base64.RawURLEncoding.EncodeToString(hb) + "." +
		base64.RawURLEncoding.EncodeToString(pb) + ".cGFzdG8"

	if _, err := w.Zweryfikuj(context.Background(), token); !errors.Is(err, ErrTokenNiepoprawny) {
		t.Fatalf("err = %v, chcę odrzucenia tokenu z alg=HS256", err)
	}
}

func TestTokenZeZmienionaTrescia(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	token := idp.podpisz(t, roszczeniaPoprawne(idp))
	czesci := strings.Split(token, ".")

	// Podmieniamy scope na szerszy, zostawiając oryginalny podpis.
	zmienione, _ := json.Marshal(map[string]any{
		"iss": idp.srv.URL, "sub": "atakujacy", "aud": "systim-mcp",
		"exp": time.Now().Add(time.Hour).Unix(), "scope": "systim:faktury admin",
	})
	sfalszowany := czesci[0] + "." + base64.RawURLEncoding.EncodeToString(zmienione) + "." + czesci[2]

	if _, err := w.Zweryfikuj(context.Background(), sfalszowany); !errors.Is(err, ErrTokenNiepoprawny) {
		t.Fatalf("err = %v, chcę ErrTokenNiepoprawny", err)
	}
}

func TestTokenNiepoprawnyFormat(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	for _, token := range []string{"", "   ", "abc", "a.b", "a.b.c.d", "@@@.@@@.@@@"} {
		if _, err := w.Zweryfikuj(context.Background(), token); err == nil {
			t.Errorf("token %q został przyjęty", token)
		}
	}
}

func TestJWKSPobieraneRazIcache(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	for range 5 {
		if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, roszczeniaPoprawne(idp))); err != nil {
			t.Fatalf("Zweryfikuj = %v", err)
		}
	}
	if got := idp.pobrania.Load(); got != 1 {
		t.Errorf("pobrań JWKS = %d, chcę 1 — zestaw kluczy ma być cache'owany", got)
	}
}

func TestJWKSNieJestDobijaneNieznanymKid(t *testing.T) {
	// Zalew tokenów z nieznanym kid nie może zamienić się w zalew żądań do IdP.
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	// Pierwsze wywołanie napełnia cache.
	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, roszczeniaPoprawne(idp))); err != nil {
		t.Fatalf("Zweryfikuj = %v", err)
	}
	pierwsze := idp.pobrania.Load()

	for range 10 {
		token := podpiszRS256(t, idp.klucz, "nieznany-kid", roszczeniaPoprawne(idp))
		if _, err := w.Zweryfikuj(context.Background(), token); err == nil {
			t.Fatal("token z nieznanym kid został przyjęty")
		}
	}
	// Dopuszczamy jedno dodatkowe pobranie (próba odnalezienia nowego klucza),
	// ale nie dziesięć.
	if got := idp.pobrania.Load(); got > pierwsze+1 {
		t.Errorf("pobrań JWKS = %d, chcę najwyżej %d — brak ograniczenia częstotliwości", got, pierwsze+1)
	}
}

func TestJWKSRotacjaKluczy(t *testing.T) {
	idp := nowyIdP(t)
	z := NoweZrodloKluczy(idp.srv.URL, idp.srv.Client(), nil)
	w := NowyWalidator(z, idp.srv.URL, "systim-mcp", "systim:faktury")

	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, roszczeniaPoprawne(idp))); err != nil {
		t.Fatalf("Zweryfikuj = %v", err)
	}

	// IdP rotuje klucz. Nowy kid wymusza ponowne pobranie zestawu.
	nowy, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generowanie klucza: %v", err)
	}
	idp.klucz = nowy
	idp.kid = "klucz-2"
	// Cofamy limit częstotliwości, żeby nie czekać minuty.
	z.mu.Lock()
	z.ostatniaProba = time.Now().Add(-2 * minOdstepOdswiezen)
	z.mu.Unlock()

	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, roszczeniaPoprawne(idp))); err != nil {
		t.Fatalf("Zweryfikuj po rotacji klucza = %v, chcę sukcesu", err)
	}
}

func TestKluczRSAPonizej2048BitowJestOdrzucany(t *testing.T) {
	k := jwk{
		Kty: "RSA",
		Kid: "slaby",
		N:   base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes()),
	}
	if _, err := k.naKluczPubliczny(); err == nil {
		t.Error("krótki klucz RSA został przyjęty")
	}
}

func TestKluczECPozaKrzywaJestOdrzucany(t *testing.T) {
	prv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generowanie klucza: %v", err)
	}
	k := jwk{
		Kty: "EC",
		Kid: "ec",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(prv.X.Bytes()),
		// Y podmienione — punkt nie leży na krzywej.
		Y: base64.RawURLEncoding.EncodeToString(big.NewInt(12345).Bytes()),
	}
	if _, err := k.naKluczPubliczny(); err == nil {
		t.Error("punkt spoza krzywej został przyjęty")
	}
}

func TestMetadaneIdPWymagajaS256(t *testing.T) {
	idp := nowyIdP(t)
	m, err := PobierzMetadaneIdP(context.Background(), idp.srv.Client(), idp.srv.URL)
	if err != nil {
		t.Fatalf("PobierzMetadaneIdP = %v", err)
	}
	if !m.ObslugujeS256() {
		t.Error("ObslugujeS256 = false; Claude wymaga PKCE metodą S256")
	}
	if m.JWKSURI == "" {
		t.Error("brak jwks_uri w metadanych")
	}
}

func TestZadanieBezTokenuDostaje401ZNaglowkiem(t *testing.T) {
	idp := nowyIdP(t)
	chroniony := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		Opcje{
			Walidator:     walidatorDo(t, idp, "systim-mcp", "systim:faktury"),
			URLMetadanych: "https://mcp.firma.pl/.well-known/oauth-protected-resource",
			WymaganyScope: "systim:faktury",
		},
	)
	srv := httptest.NewServer(chroniony)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("kod = %d, chcę 401", resp.StatusCode)
	}
	wa := resp.Header.Get("WWW-Authenticate")
	if wa == "" {
		t.Fatal("brak nagłówka WWW-Authenticate")
	}
	// Claude odnajduje serwer autoryzacji właśnie po resource_metadata.
	for _, fragment := range []string{"Bearer", `resource_metadata="https://mcp.firma.pl/.well-known/oauth-protected-resource"`, `scope="systim:faktury"`} {
		if !strings.Contains(wa, fragment) {
			t.Errorf("WWW-Authenticate = %q, brakuje %q", wa, fragment)
		}
	}
}

func TestZadanieZeZlymTokenemDostaje401(t *testing.T) {
	idp := nowyIdP(t)
	chroniony := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		Opcje{
			Walidator:     walidatorDo(t, idp, "systim-mcp", "systim:faktury"),
			URLMetadanych: "https://mcp.firma.pl/.well-known/oauth-protected-resource",
			WymaganyScope: "systim:faktury",
		},
	)
	srv := httptest.NewServer(chroniony)
	defer srv.Close()

	przypadki := []struct {
		nazwa string
		auth  string
		kod   int
	}{
		{"pusty nagłówek", "", http.StatusUnauthorized},
		{"zły schemat", "Basic YWJjOmRlZg==", http.StatusUnauthorized},
		{"śmieciowy token", "Bearer abc.def.ghi", http.StatusUnauthorized},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
			if p.auth != "" {
				req.Header.Set("Authorization", p.auth)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("Do = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != p.kod {
				t.Errorf("kod = %d, chcę %d", resp.StatusCode, p.kod)
			}
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("brak nagłówka WWW-Authenticate")
			}
		})
	}
}

func TestZadanieBezScopeDostaje403(t *testing.T) {
	idp := nowyIdP(t)
	chroniony := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		Opcje{
			Walidator:     walidatorDo(t, idp, "systim-mcp", "systim:faktury"),
			URLMetadanych: "https://mcp.firma.pl/.well-known/oauth-protected-resource",
			WymaganyScope: "systim:faktury",
		},
	)
	srv := httptest.NewServer(chroniony)
	defer srv.Close()

	r := roszczeniaPoprawne(idp)
	r["scope"] = "openid"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+idp.podpisz(t, r))

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("kod = %d, chcę 403", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "insufficient_scope") {
		t.Errorf("WWW-Authenticate = %q, chcę insufficient_scope", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestZadanieZPoprawnymTokenemPrzechodziIniesieRoszczenia(t *testing.T) {
	idp := nowyIdP(t)
	var widzianySub string
	chroniony := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if roszczenia, ok := RoszczeniaZKontekstu(r.Context()); ok {
				widzianySub = roszczenia.Subject
			}
			w.WriteHeader(http.StatusOK)
		}),
		Opcje{
			Walidator:     walidatorDo(t, idp, "systim-mcp", "systim:faktury"),
			URLMetadanych: "https://mcp.firma.pl/.well-known/oauth-protected-resource",
			WymaganyScope: "systim:faktury",
		},
	)
	srv := httptest.NewServer(chroniony)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+idp.podpisz(t, roszczeniaPoprawne(idp)))

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		ciało, _ := io.ReadAll(resp.Body)
		t.Fatalf("kod = %d (%s), chcę 200", resp.StatusCode, ciało)
	}
	if widzianySub != "uzytkownik-1" {
		t.Errorf("sub w kontekście = %q, chcę uzytkownik-1", widzianySub)
	}
}

func TestObcyOriginJestOdrzucany(t *testing.T) {
	handler := SprawdzOrigin(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		[]string{"https://mcp.firma.pl"},
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	przypadki := []struct {
		nazwa  string
		origin string
		kod    int
	}{
		{"brak Origin (żądanie serwer-serwer, tak łączy się Claude)", "", http.StatusOK},
		{"Origin dozwolony", "https://mcp.firma.pl", http.StatusOK},
		{"Origin dozwolony z ukośnikiem", "https://mcp.firma.pl/", http.StatusOK},
		{"Origin obcy", "https://zlosliwa-strona.example", http.StatusForbidden},
		{"Origin null", "null", http.StatusForbidden},
		{"atak DNS rebinding z localhost", "http://localhost:8000", http.StatusForbidden},
		{"podobna domena", "https://mcp.firma.pl.evil.example", http.StatusForbidden},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
			if p.origin != "" {
				req.Header.Set("Origin", p.origin)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("Do = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != p.kod {
				t.Errorf("Origin %q → kod %d, chcę %d", p.origin, resp.StatusCode, p.kod)
			}
		})
	}
}

func TestHandlerMetadanychZasobu(t *testing.T) {
	h := HandlerMetadanych(MetadaneZasobu{
		Resource:               "https://mcp.firma.pl/mcp",
		AuthorizationServers:   []string{"https://auth.firma.pl/realms/firma"},
		ScopesSupported:        []string{"systim:faktury"},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Systim MCP",
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kod = %d, chcę 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var m MetadaneZasobu
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("dekodowanie = %v", err)
	}
	if m.Resource != "https://mcp.firma.pl/mcp" {
		t.Errorf("resource = %q", m.Resource)
	}
	if len(m.AuthorizationServers) != 1 || m.AuthorizationServers[0] != "https://auth.firma.pl/realms/firma" {
		t.Errorf("authorization_servers = %v", m.AuthorizationServers)
	}
}

func TestAudienceJakoTablica(t *testing.T) {
	// Część dostawców wpisuje aud jako tablicę — RFC 7519 na to pozwala.
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r := roszczeniaPoprawne(idp)
	r["aud"] = []string{"inna-aplikacja", "systim-mcp"}

	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, r)); err != nil {
		t.Fatalf("Zweryfikuj = %v, chcę akceptacji aud jako tablicy", err)
	}
}

func TestScopeWPoluScp(t *testing.T) {
	idp := nowyIdP(t)
	w := walidatorDo(t, idp, "systim-mcp", "systim:faktury")

	r := roszczeniaPoprawne(idp)
	delete(r, "scope")
	r["scp"] = []string{"openid", "systim:faktury"}

	if _, err := w.Zweryfikuj(context.Background(), idp.podpisz(t, r)); err != nil {
		t.Fatalf("Zweryfikuj = %v, chcę obsługi pola scp", err)
	}
}

func TestWalidatorNilPrzepuszcza(t *testing.T) {
	// Tryb SYSTIM_AUTH_DISABLED — wyłącznie do testów lokalnych.
	h := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		Opcje{Walidator: nil},
	)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("kod = %d, chcę przepuszczenia żądania", resp.StatusCode)
	}
}

func TestOczyscOpisNieRozbijaNaglowka(t *testing.T) {
	got := oczyscOpis("token \"zły\"\nz nową linią\r i powrotem")
	if strings.ContainsAny(got, "\"\n\r") {
		t.Errorf("oczyscOpis = %q, wciąż zawiera znaki rozbijające nagłówek", got)
	}
	dlugi := oczyscOpis(strings.Repeat("x", 500))
	if len(dlugi) > 200 {
		t.Errorf("oczyscOpis nie przyciął długiego opisu: %d znaków", len(dlugi))
	}
}

func TestPobierzMetadaneIdPOdrzucaNiezgodnyIssuer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"issuer":"https://ktos-inny.example","jwks_uri":"https://ktos-inny.example/jwks"}`)
	}))
	defer srv.Close()

	_, err := PobierzMetadaneIdP(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("PobierzMetadaneIdP = nil, chcę błędu przy niezgodnym issuerze")
	}
	if !strings.Contains(err.Error(), "issuer") {
		t.Errorf("err = %v", err)
	}
}

func TestNaglowekOglaszaScopeSzerszyNizWymagany(t *testing.T) {
	// Parametr scope w WWW-Authenticate mówi klientowi, o co poprosić serwer
	// autoryzacji, i bywa szerszy niż to, co sprawdzamy w tokenie. Przy authentiku
	// jest to konieczne: bez offline_access nie dostaniemy refresh tokenu.
	idp := nowyIdP(t)
	chroniony := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		Opcje{
			Walidator:     walidatorDo(t, idp, "systim-mcp", "systim:faktury"),
			URLMetadanych: "https://mcp.firma.pl/.well-known/oauth-protected-resource",
			WymaganyScope: "systim:faktury",
			ZadaneScopes:  []string{"systim:faktury", "openid", "offline_access"},
		},
	)
	srv := httptest.NewServer(chroniony)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("Post = %v", err)
	}
	defer resp.Body.Close()

	wa := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wa, `scope="systim:faktury openid offline_access"`) {
		t.Errorf("WWW-Authenticate = %q, chcę pełnej listy żądanych scope'ów", wa)
	}

	// Token nadal musi mieć wyłącznie scope wymagany — offline_access nie jest
	// warunkiem dostępu, tylko prośbą skierowaną do serwera autoryzacji.
	r := roszczeniaPoprawne(idp)
	r["scope"] = "systim:faktury"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+idp.podpisz(t, r))
	resp2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("kod = %d, chcę 200 — offline_access nie może być wymagany w tokenie", resp2.StatusCode)
	}
}

func TestBrakZadanychScopesOglaszaWymagany(t *testing.T) {
	idp := nowyIdP(t)
	chroniony := Wymagaj(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		Opcje{
			Walidator:     walidatorDo(t, idp, "systim-mcp", "systim:faktury"),
			WymaganyScope: "systim:faktury",
		},
	)
	srv := httptest.NewServer(chroniony)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("Post = %v", err)
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), `scope="systim:faktury"`) {
		t.Errorf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
}
