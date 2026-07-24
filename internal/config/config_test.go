package config

import (
	"strings"
	"testing"
	"time"
)

// ustawMinimalne ustawia komplet zmiennych wymaganych do poprawnego startu w trybie http.
func ustawMinimalne(t *testing.T) {
	t.Helper()
	t.Setenv("SYSTIM_KONTO", "abcd")
	t.Setenv("SYSTIM_LOGIN", "api_user")
	t.Setenv("SYSTIM_PASS", "haslo_api")
	t.Setenv("SYSTIM_ID_SZABLONU", "3")
	t.Setenv("SYSTIM_ID_NUMERACJI", "5")
	t.Setenv("SYSTIM_VAT_IDS", `{"23":1,"8":2,"5":3,"0":4,"zw":5}`)
	t.Setenv("SYSTIM_SZKIC_KLUCZ", strings.Repeat("k", 48))
	t.Setenv("SYSTIM_PUBLIC_URL", "https://mcp.firma.pl")
	t.Setenv("OIDC_ISSUER", "https://auth.firma.pl/realms/firma")
	t.Setenv("OIDC_AUDIENCE", "systim-mcp")
	t.Setenv("OIDC_SCOPE", "systim:faktury")
}

func TestWczytajKonfiguracjaMinimalna(t *testing.T) {
	ustawMinimalne(t)

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if c.Transport != TransportHTTP {
		t.Errorf("Transport = %q, chcę domyślnie http", c.Transport)
	}
	if c.Adres != DomyslnyAdres {
		t.Errorf("Adres = %q, chcę %q", c.Adres, DomyslnyAdres)
	}
	if c.Timeout != DomyslnyTimeout {
		t.Errorf("Timeout = %s, chcę %s", c.Timeout, DomyslnyTimeout)
	}
	if c.VatIDs["23"] != 1 || c.VatIDs["zw"] != 5 {
		t.Errorf("VatIDs = %v", c.VatIDs)
	}
	if c.URLSystim() != "https://abcd.systim.pl/jsonAPI" {
		t.Errorf("URLSystim = %q", c.URLSystim())
	}
	if c.URLZasobu() != "https://mcp.firma.pl/mcp" {
		t.Errorf("URLZasobu = %q", c.URLZasobu())
	}
	if c.MaxPozycji != DomyslnyMaxPozycji {
		t.Errorf("MaxPozycji = %d, chcę %d", c.MaxPozycji, DomyslnyMaxPozycji)
	}
}

func TestWczytajZglaszaWszystkieBrakiNaraz(t *testing.T) {
	// Sens: administrator ma zobaczyć pełną listę braków od razu, a nie odkrywać
	// je po jednym przy kolejnych restartach.
	t.Setenv("SYSTIM_KONTO", "")
	t.Setenv("SYSTIM_LOGIN", "")
	t.Setenv("SYSTIM_PASS", "")
	t.Setenv("SYSTIM_ID_SZABLONU", "")
	t.Setenv("SYSTIM_ID_NUMERACJI", "")
	t.Setenv("SYSTIM_VAT_IDS", "")
	t.Setenv("SYSTIM_SZKIC_KLUCZ", "")
	t.Setenv("SYSTIM_PUBLIC_URL", "")
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("OIDC_SCOPE", "")

	_, err := Wczytaj()
	if err == nil {
		t.Fatal("Wczytaj = nil, chcę błędu przy pustej konfiguracji")
	}
	wymagane := []string{
		"SYSTIM_KONTO", "SYSTIM_LOGIN", "SYSTIM_PASS", "SYSTIM_ID_SZABLONU",
		"SYSTIM_ID_NUMERACJI", "SYSTIM_VAT_IDS", "SYSTIM_SZKIC_KLUCZ",
		"SYSTIM_PUBLIC_URL", "OIDC_ISSUER", "OIDC_AUDIENCE", "OIDC_SCOPE",
	}
	for _, w := range wymagane {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("komunikat nie wymienia brakującej zmiennej %s:\n%v", w, err)
		}
	}
	if !strings.Contains(err.Error(), ".env.example") {
		t.Error("komunikat nie odsyła do .env.example")
	}
}

func TestWczytajKontoZKropkaJestOdrzucane(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_KONTO", "abcd.systim.pl")

	_, err := Wczytaj()
	if err == nil {
		t.Fatal("Wczytaj = nil, chcę błędu dla pełnego adresu w SYSTIM_KONTO")
	}
	if !strings.Contains(err.Error(), "samą poddomenę") {
		t.Errorf("err = %v, chcę podpowiedzi o poddomenie", err)
	}
}

func TestWczytajVatIDsWarianty(t *testing.T) {
	przypadki := []struct {
		nazwa string
		json  string
		ok    bool
	}{
		{"liczby", `{"23":1,"zw":5}`, true},
		{"wartości jako stringi", `{"23":"1","zw":"5"}`, true},
		{"niepoprawny JSON", `{23:1}`, false},
		{"wartość nieliczbowa", `{"23":"jeden"}`, false},
		{"pusty obiekt", `{}`, false},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			ustawMinimalne(t)
			t.Setenv("SYSTIM_VAT_IDS", p.json)
			c, err := Wczytaj()
			if p.ok {
				if err != nil {
					t.Fatalf("Wczytaj = %v, chcę sukcesu", err)
				}
				if c.VatIDs["23"] != 1 {
					t.Errorf("VatIDs = %v", c.VatIDs)
				}
				return
			}
			if err == nil {
				t.Fatalf("Wczytaj = nil, chcę błędu dla %s", p.json)
			}
		})
	}
}

func TestWczytajPublicURLMusiBycHTTPS(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_PUBLIC_URL", "http://mcp.firma.pl")

	_, err := Wczytaj()
	if err == nil {
		t.Fatal("Wczytaj = nil, chcę błędu dla adresu bez HTTPS")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("err = %v, chcę wzmianki o HTTPS", err)
	}

	// localhost jest wyjątkiem — do testów lokalnych.
	t.Setenv("SYSTIM_PUBLIC_URL", "http://localhost:8000")
	if _, err := Wczytaj(); err != nil {
		t.Errorf("Wczytaj = %v, chcę dopuszczenia http na localhost", err)
	}
}

func TestWczytajStdioNieWymagaUstawienHTTP(t *testing.T) {
	// Tryb lokalnego debugowania nie potrzebuje ani publicznego URL-a, ani IdP.
	t.Setenv("SYSTIM_KONTO", "abcd")
	t.Setenv("SYSTIM_LOGIN", "api_user")
	t.Setenv("SYSTIM_PASS", "haslo_api")
	t.Setenv("SYSTIM_ID_SZABLONU", "3")
	t.Setenv("SYSTIM_ID_NUMERACJI", "5")
	t.Setenv("SYSTIM_VAT_IDS", `{"23":1}`)
	t.Setenv("SYSTIM_SZKIC_KLUCZ", strings.Repeat("k", 48))
	t.Setenv("SYSTIM_TRANSPORT", "stdio")
	t.Setenv("SYSTIM_PUBLIC_URL", "")
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("OIDC_SCOPE", "")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v, chcę sukcesu dla transportu stdio", err)
	}
	if c.Transport != TransportStdio {
		t.Errorf("Transport = %q, chcę stdio", c.Transport)
	}
}

func TestWczytajAuthDisabledZwalniaZUstawienOIDC(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("OIDC_SCOPE", "")
	t.Setenv("SYSTIM_AUTH_DISABLED", "true")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v, chcę sukcesu przy wyłączonej autoryzacji", err)
	}
	if !c.AuthDisabled {
		t.Error("AuthDisabled = false, chcę true")
	}
	// SYSTIM_PUBLIC_URL zostaje wymagany — potrzebny do walidacji Origin.
	t.Setenv("SYSTIM_PUBLIC_URL", "")
	if _, err := Wczytaj(); err == nil {
		t.Error("Wczytaj = nil; SYSTIM_PUBLIC_URL ma być wymagany także przy wyłączonej autoryzacji")
	}
}

func TestWczytajKluczSzkicowMinimalnaDlugosc(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_SZKIC_KLUCZ", "za krótki klucz")

	_, err := Wczytaj()
	if err == nil {
		t.Fatal("Wczytaj = nil, chcę błędu dla krótkiego klucza")
	}
	if !strings.Contains(err.Error(), "32") || !strings.Contains(err.Error(), "openssl") {
		t.Errorf("err = %v, chcę informacji o minimum 32 bajtów i podpowiedzi z openssl", err)
	}
}

func TestWczytajWartosciNiepoprawne(t *testing.T) {
	przypadki := []struct {
		zmienna, wartosc, fragment string
	}{
		{"SYSTIM_TRANSPORT", "grpc", "http albo stdio"},
		{"LOG_LEVEL", "gadatliwy", "debug, info, warn, error"},
		{"SYSTIM_TIMEOUT", "trzydzieści sekund", "poprawnym czasem"},
		{"SYSTIM_TIMEOUT", "-5s", "dodatnie"},
		{"SYSTIM_MAX_POZYCJI", "zero", "liczbą całkowitą"},
		{"SYSTIM_MAX_POZYCJI", "0", "większe od zera"},
		{"SYSTIM_AUTH_DISABLED", "może", "wartością logiczną"},
	}
	for _, p := range przypadki {
		t.Run(p.zmienna+"="+p.wartosc, func(t *testing.T) {
			ustawMinimalne(t)
			t.Setenv(p.zmienna, p.wartosc)
			_, err := Wczytaj()
			if err == nil {
				t.Fatalf("Wczytaj = nil, chcę błędu dla %s=%q", p.zmienna, p.wartosc)
			}
			if !strings.Contains(err.Error(), p.fragment) {
				t.Errorf("err = %v, chcę wzmianki o %q", err, p.fragment)
			}
		})
	}
}

func TestWczytajWartosciOpcjonalne(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_TIMEOUT", "45s")
	t.Setenv("SYSTIM_ADDR", ":9000")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SYSTIM_MAX_POZYCJI", "50")
	t.Setenv("SYSTIM_DODATKOWE_ORIGINY", "https://claude.ai, https://example.com/")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if c.Timeout != 45*time.Second {
		t.Errorf("Timeout = %s", c.Timeout)
	}
	if c.Adres != ":9000" {
		t.Errorf("Adres = %q", c.Adres)
	}
	if c.MaxPozycji != 50 {
		t.Errorf("MaxPozycji = %d", c.MaxPozycji)
	}
	oczekiwane := []string{"https://mcp.firma.pl", "https://claude.ai", "https://example.com"}
	got := c.DozwoloneOriginy()
	if len(got) != len(oczekiwane) {
		t.Fatalf("DozwoloneOriginy = %v, chcę %v", got, oczekiwane)
	}
	for _, o := range oczekiwane {
		found := false
		for _, g := range got {
			if g == o {
				found = true
			}
		}
		if !found {
			t.Errorf("DozwoloneOriginy = %v, brakuje %q", got, o)
		}
	}
}

func TestPublicURLBezKoncowegoUkosnika(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_PUBLIC_URL", "https://mcp.firma.pl/")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if c.PublicURL != "https://mcp.firma.pl" {
		t.Errorf("PublicURL = %q, chcę bez końcowego ukośnika", c.PublicURL)
	}
	if c.URLMetadanychZasobu() != "https://mcp.firma.pl/.well-known/oauth-protected-resource" {
		t.Errorf("URLMetadanychZasobu = %q", c.URLMetadanychZasobu())
	}
}

func TestScopesZadaneDomyslnieZawierajaOfflineAccess(t *testing.T) {
	// authentik od wersji 2024.2 nie wyda refresh tokenu, jeśli klient nie poprosi
	// o scope offline_access. Bez refresh tokenu konektor Claude rozłącza się,
	// gdy tylko wygaśnie token dostępowy — objaw mylący, więc pilnujemy domyślnej wartości.
	ustawMinimalne(t)

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if len(c.OIDCScopesZadane) == 0 {
		t.Fatal("OIDCScopesZadane jest puste")
	}
	// Wymagany scope musi być pierwszy — to on jest powodem tej autoryzacji.
	if c.OIDCScopesZadane[0] != "systim:faktury" {
		t.Errorf("OIDCScopesZadane = %v, chcę systim:faktury na pierwszym miejscu", c.OIDCScopesZadane)
	}
	for _, chce := range []string{"systim:faktury", "openid", "offline_access"} {
		if !zawiera(c.OIDCScopesZadane, chce) {
			t.Errorf("OIDCScopesZadane = %v, brakuje %q", c.OIDCScopesZadane, chce)
		}
	}
}

func TestScopesZadaneMoznaNadpisac(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("OIDC_SCOPES_REQUESTED", "openid, offline_access, profile")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	// Wymagany scope jest dopisywany zawsze — bez niego żaden token nie przeszedłby walidacji.
	if !zawiera(c.OIDCScopesZadane, "systim:faktury") {
		t.Errorf("OIDCScopesZadane = %v, wymagany scope musi być dopisany zawsze", c.OIDCScopesZadane)
	}
	if !zawiera(c.OIDCScopesZadane, "profile") {
		t.Errorf("OIDCScopesZadane = %v, brakuje scope z nadpisania", c.OIDCScopesZadane)
	}
}

func TestScopesZadaneBezDuplikatow(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("OIDC_SCOPES_REQUESTED", "systim:faktury openid openid offline_access")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	widziane := map[string]int{}
	for _, s := range c.OIDCScopesZadane {
		widziane[s]++
	}
	for s, n := range widziane {
		if n > 1 {
			t.Errorf("scope %q występuje %d razy w %v", s, n, c.OIDCScopesZadane)
		}
	}
}

func zawiera(lista []string, v string) bool {
	for _, s := range lista {
		if s == v {
			return true
		}
	}
	return false
}
