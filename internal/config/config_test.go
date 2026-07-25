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

func TestNumeracjaJakoMapaRodzajNaID(t *testing.T) {
	// Systim wiąże serię numeracji z typem dokumentu. Wysłanie numeracji faktury VAT
	// przy rodzaju „pro forma" kończy się odrzuceniem dokumentu komunikatem
	// „błędne przypisanie rodzaju dokumentu do numeracji".
	ustawMinimalne(t)
	t.Setenv("SYSTIM_ID_NUMERACJI", `{"0":1,"1":5}`)

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	for _, p := range []struct {
		rodzaj int
		chce   string
	}{
		{0, "1"},   // faktura VAT
		{1, "5"},   // pro forma
		{22, "16"}, // rachunek — uzupełnione z wartości domyślnych
		{26, "21"}, // oferta — j.w.
	} {
		got, err := c.Numeracja(p.rodzaj)
		if err != nil {
			t.Errorf("Numeracja(%d) = %v", p.rodzaj, err)
			continue
		}
		if got != p.chce {
			t.Errorf("Numeracja(%d) = %q, chcę %q", p.rodzaj, got, p.chce)
		}
	}
}

func TestNumeracjaPojedynczaWartoscDlaWszystkich(t *testing.T) {
	// Zgodność wsteczna: pojedyncza liczba obowiązuje dla każdego rodzaju.
	ustawMinimalne(t)
	t.Setenv("SYSTIM_ID_NUMERACJI", "7")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	for _, rodzaj := range []int{0, 1, 22} {
		got, err := c.Numeracja(rodzaj)
		if err != nil {
			t.Fatalf("Numeracja(%d) = %v", rodzaj, err)
		}
		if got != "7" {
			t.Errorf("Numeracja(%d) = %q, chcę 7", rodzaj, got)
		}
	}
}

func TestNumeracjaDomyslneGdyMapaNiepelna(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_ID_NUMERACJI", `{"0":99}`)

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if got, _ := c.Numeracja(0); got != "99" {
		t.Errorf("Numeracja(0) = %q, chcę 99 — nadpisanie z konfiguracji", got)
	}
	// Pro forma nie została nadpisana, więc bierzemy wartość domyślną.
	if got, _ := c.Numeracja(1); got != "5" {
		t.Errorf("Numeracja(1) = %q, chcę 5 z wartości domyślnych", got)
	}
}

func TestNumeracjaBrakWpisuDajeCzytelnyBlad(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_ID_NUMERACJI", `{"0":1}`)

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	// Rodzaj spoza wartości domyślnych i spoza konfiguracji.
	_, err = c.Numeracja(999)
	if err == nil {
		t.Fatal("Numeracja(999) = nil, chcę błędu")
	}
	for _, fragment := range []string{"999", "SYSTIM_ID_NUMERACJI", "Numeracja dokumentów"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("err = %v, chcę wzmianki o %q", err, fragment)
		}
	}
}

func TestNumeracjaWartosciNiepoprawne(t *testing.T) {
	przypadki := []struct{ wartosc, fragment string }{
		{"", "SYSTIM_ID_NUMERACJI"},
		{"0", "dodatnią"},
		{"-3", "dodatnią"},
		{`{"0":"zero"}`, "nie jest dodatnią"},
		{`{"abc":1}`, "nie jest numerem rodzaju"},
		{"nie-json", "ani liczbą, ani poprawnym JSON-em"},
	}
	for _, p := range przypadki {
		t.Run(p.wartosc, func(t *testing.T) {
			ustawMinimalne(t)
			t.Setenv("SYSTIM_ID_NUMERACJI", p.wartosc)
			_, err := Wczytaj()
			if err == nil {
				t.Fatalf("Wczytaj = nil, chcę błędu dla %q", p.wartosc)
			}
			if !strings.Contains(err.Error(), p.fragment) {
				t.Errorf("err = %v, chcę wzmianki o %q", err, p.fragment)
			}
		})
	}
}

func TestNumeracjeDomyslneObejmujaWszystkieObslugiwaneRodzaje(t *testing.T) {
	// Każdy rodzaj, który serwer potrafi wystawić, musi mieć domyślną numerację —
	// inaczej użytkownik trafi na błąd dopiero przy pierwszym dokumencie tego typu.
	for _, rodzaj := range []int{0, 1, 6, 15, 22, 26} {
		if _, ok := NumeracjeDomyslne[rodzaj]; !ok {
			t.Errorf("brak domyślnej numeracji dla rodzaju %d", rodzaj)
		}
	}
}

func TestSzablonDobieranyDoRodzajuDokumentu(t *testing.T) {
	// Szablon, tak samo jak numeracja, jest w Systim przypisany do typu dokumentu.
	ustawMinimalne(t)
	t.Setenv("SYSTIM_ID_SZABLONU", `{"0":43,"1":1}`)

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	for _, p := range []struct {
		rodzaj int
		chce   string
	}{
		{0, "43"},  // faktura VAT
		{1, "1"},   // pro forma
		{22, "22"}, // rachunek — z wartości domyślnych
		{26, "26"}, // oferta — j.w.
	} {
		got, err := c.Szablon(p.rodzaj)
		if err != nil {
			t.Errorf("Szablon(%d) = %v", p.rodzaj, err)
			continue
		}
		if got != p.chce {
			t.Errorf("Szablon(%d) = %q, chcę %q", p.rodzaj, got, p.chce)
		}
	}
}

func TestSzablonBrakWpisuDajeCzytelnyBlad(t *testing.T) {
	ustawMinimalne(t)
	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	_, err = c.Szablon(999)
	if err == nil {
		t.Fatal("Szablon(999) = nil, chcę błędu")
	}
	for _, fragment := range []string{"999", "SYSTIM_ID_SZABLONU", "Szablony wydruku"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("err = %v, chcę wzmianki o %q", err, fragment)
		}
	}
}

func TestSzablonyINumeracjePokrywajaTeSameRodzaje(t *testing.T) {
	// Rozjazd list oznaczałby, że któryś rodzaj da się przygotować, ale nie wystawić.
	for rodzaj := range NumeracjeDomyslne {
		if _, ok := SzablonyDomyslne[rodzaj]; !ok {
			t.Errorf("rodzaj %d ma domyślną numerację, ale nie ma domyślnego szablonu", rodzaj)
		}
	}
	for rodzaj := range SzablonyDomyslne {
		if _, ok := NumeracjeDomyslne[rodzaj]; !ok {
			t.Errorf("rodzaj %d ma domyślny szablon, ale nie ma domyślnej numeracji", rodzaj)
		}
	}
}

func TestDomyslnaFormaPlatnosci(t *testing.T) {
	// Bez tego ustawienia pominięcie formy płatności daje wartość domyślną Systim
	// (gotówkę), co przy firmie rozliczającej się przelewem jest cichą pomyłką.
	ustawMinimalne(t)
	t.Setenv("SYSTIM_DOMYSLNA_FORMA_PLATNOSCI", "przelew")

	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if c.DomyslnaFormaPlatnosci != "przelew" {
		t.Errorf("DomyslnaFormaPlatnosci = %q, chcę przelew", c.DomyslnaFormaPlatnosci)
	}
}

func TestDomyslnaFormaPlatnosciOpcjonalnaIWalidowana(t *testing.T) {
	ustawMinimalne(t)
	if c, err := Wczytaj(); err != nil || c.DomyslnaFormaPlatnosci != "" {
		t.Errorf("brak zmiennej ma dawać pustą wartość, dostałem %q / %v", c.DomyslnaFormaPlatnosci, err)
	}

	// Wielkość liter nie ma znaczenia.
	ustawMinimalne(t)
	t.Setenv("SYSTIM_DOMYSLNA_FORMA_PLATNOSCI", "PRZELEW")
	if c, err := Wczytaj(); err != nil || c.DomyslnaFormaPlatnosci != "przelew" {
		t.Errorf("PRZELEW → %q / %v, chcę przelew", c.DomyslnaFormaPlatnosci, err)
	}

	// Nieznana wartość jest błędem konfiguracji, a nie cichym pominięciem.
	ustawMinimalne(t)
	t.Setenv("SYSTIM_DOMYSLNA_FORMA_PLATNOSCI", "blik")
	_, err := Wczytaj()
	if err == nil {
		t.Fatal("Wczytaj = nil, chcę błędu dla nieobsługiwanej formy płatności")
	}
	if !strings.Contains(err.Error(), "przelew") {
		t.Errorf("err = %v, chcę listy dozwolonych form", err)
	}
}

func TestFormatFormyPlatnosciNazwaAlboID(t *testing.T) {
	// Dokumentacja Systim mówi „nazwa", ale nazwa nie odnosi skutku. Przełącznik
	// pozwala sprawdzić wariant z ID bez zmiany kodu.
	ustawMinimalne(t)
	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if c.FormaPlatnosciJakoID {
		t.Error("domyślnie ma obowiązywać format zgodny z dokumentacją, czyli nazwa")
	}
	if got := c.WartoscFormyPlatnosci("przelew"); got != "przelew" {
		t.Errorf("WartoscFormyPlatnosci = %q, chcę przelew", got)
	}

	ustawMinimalne(t)
	t.Setenv("SYSTIM_FORMA_PLATNOSCI_FORMAT", "id")
	c, err = Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if !c.FormaPlatnosciJakoID {
		t.Fatal("FormaPlatnosciJakoID = false, chcę true")
	}
	for nazwa, chce := range map[string]string{
		"przelew": "1", "gotówka": "2", "barter": "3",
		"za pobraniem": "4", "rozliczenie saldami": "5", "karta płatnicza": "6",
	} {
		if got := c.WartoscFormyPlatnosci(nazwa); got != chce {
			t.Errorf("WartoscFormyPlatnosci(%q) = %q, chcę %q", nazwa, got, chce)
		}
	}
	// Wielkość liter nie ma znaczenia.
	if got := c.WartoscFormyPlatnosci("PRZELEW"); got != "1" {
		t.Errorf("WartoscFormyPlatnosci(PRZELEW) = %q, chcę 1", got)
	}
}

func TestFormatFormyPlatnosciWalidacjaINadpisanie(t *testing.T) {
	ustawMinimalne(t)
	t.Setenv("SYSTIM_FORMA_PLATNOSCI_FORMAT", "cokolwiek")
	if _, err := Wczytaj(); err == nil {
		t.Fatal("Wczytaj = nil, chcę błędu dla nieznanego formatu")
	}

	// ID da się nadpisać, gdy konto ma inną kartotekę.
	ustawMinimalne(t)
	t.Setenv("SYSTIM_FORMA_PLATNOSCI_FORMAT", "id")
	t.Setenv("SYSTIM_FORMY_PLATNOSCI", `{"przelew":9}`)
	c, err := Wczytaj()
	if err != nil {
		t.Fatalf("Wczytaj = %v", err)
	}
	if got := c.WartoscFormyPlatnosci("przelew"); got != "9" {
		t.Errorf("WartoscFormyPlatnosci(przelew) = %q, chcę 9", got)
	}
	if got := c.WartoscFormyPlatnosci("gotówka"); got != "2" {
		t.Errorf("WartoscFormyPlatnosci(gotówka) = %q, chcę 2 z wartości domyślnych", got)
	}
}
