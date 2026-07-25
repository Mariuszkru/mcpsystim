// Package config czyta i waliduje konfigurację z zmiennych środowiskowych.
//
// Walidacja odbywa się raz, przy starcie procesu. Brak wymaganej zmiennej ma
// zatrzymać serwer z czytelnym komunikatem, a nie wysypać się dopiero przy pierwszym
// wywołaniu narzędzia — wtedy błąd zobaczy model zamiast administratora.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Transport określa sposób komunikacji z klientem MCP.
type Transport string

const (
	// TransportHTTP to docelowy tryb pracy: serwer zdalny na Streamable HTTP.
	TransportHTTP Transport = "http"
	// TransportStdio służy do lokalnego debugowania.
	TransportStdio Transport = "stdio"
)

// Domyślne wartości konfiguracji.
const (
	DomyslnyAdres      = ":8000"
	DomyslnyKatalogPDF = "/data/faktury"
	DomyslnyTimeout    = 30 * time.Second
	DomyslnyLogLevel   = "info"
	// DomyslnyMaxPozycji ogranicza rozmiar jednego dokumentu.
	DomyslnyMaxPozycji = 200
	// DomyslnyMaxCialo ogranicza rozmiar żądania HTTP do /mcp.
	DomyslnyMaxCialo int64 = 4 << 20 // 4 MiB
)

// Config to komplet ustawień serwera.
type Config struct {
	// Dostęp do Systim.
	Konto string
	Login string
	Pass  string
	// IDSzablonu mapuje rodzaj dokumentu na ID szablonu wydruku.
	//
	// Szablon jest w Systim przypisany do typu dokumentu — tak samo jak numeracja.
	IDSzablonu map[int]string
	// IDNumeracji mapuje rodzaj dokumentu na ID serii numeracji w Systim.
	//
	// W Systim seria numeracji jest przypisana do konkretnego typu dokumentu.
	// Wysłanie ID serii nieodpowiadającej polu "rodzaj" kończy się odrzuceniem
	// dokumentu komunikatem „błędne przypisanie rodzaju dokumentu do numeracji",
	// dlatego jedna wartość dla wszystkich rodzajów nie wystarcza.
	IDNumeracji map[int]string
	VatIDs      map[string]int
	Timeout     time.Duration
	// DomyslnaFormaPlatnosci jest używana, gdy narzędzie nie poda formy płatności.
	// Puste oznacza „nie wysyłaj pola" — wtedy zadziała wartość domyślna Systim.
	DomyslnaFormaPlatnosci string

	// Serwer.
	Transport  Transport
	Adres      string
	KatalogPDF string
	PublicURL  string
	LogLevel   slog.Level

	// Bezpieczeństwo.
	SzkicKlucz   []byte
	OIDCIssuer   string
	OIDCAudience string
	// OIDCScope to uprawnienie sprawdzane w tokenie dostępowym.
	OIDCScope string
	// OIDCScopesZadane to scope'y ogłaszane klientowi w nagłówku WWW-Authenticate
	// i w Protected Resource Metadata. Zwykle jest ich więcej niż OIDCScope —
	// patrz DomyslneScopesZadane.
	OIDCScopesZadane []string
	AuthDisabled     bool
	DodatkoweOrig    []string

	// Limity.
	MaxPozycji int
	MaxCialo   int64

	// WylaczOchroneLocalhost wyłącza wbudowaną w SDK ochronę odrzucającą żądania
	// z adresu localhost o nielokalnym nagłówku Host. Potrzebne, gdy przed
	// kontenerem stoi reverse proxy na tym samym hoście.
	WylaczOchroneLocalhost bool
}

// zbieraczBledow gromadzi wszystkie problemy z konfiguracją, żeby zgłosić je naraz.
// Poprawianie zmiennych po jednej, restart po restarcie, jest zbędnie uciążliwe.
type zbieraczBledow struct {
	bledy []string
}

func (z *zbieraczBledow) dodaj(format string, args ...any) {
	z.bledy = append(z.bledy, fmt.Sprintf(format, args...))
}

func (z *zbieraczBledow) blad() error {
	if len(z.bledy) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("błędna konfiguracja serwera:\n")
	for _, e := range z.bledy {
		b.WriteString("  • " + e + "\n")
	}
	b.WriteString("\nWzór konfiguracji znajdziesz w pliku .env.example.")
	return fmt.Errorf("%s", b.String())
}

// Wczytaj odczytuje konfigurację ze środowiska i waliduje ją w całości.
func Wczytaj() (*Config, error) {
	var z zbieraczBledow
	c := &Config{}

	// --- dostęp do Systim ---
	c.Konto = wymagane(&z, "SYSTIM_KONTO", "poddomena konta w Systim, np. abcd dla abcd.systim.pl")
	c.Login = wymagane(&z, "SYSTIM_LOGIN", "użytkownik z wygenerowanym hasłem API")
	c.Pass = wymagane(&z, "SYSTIM_PASS", "hasło do API (generowane w panelu, inne niż hasło do logowania)")
	c.IDSzablonu = wczytajMapeRodzajow(&z, "SYSTIM_ID_SZABLONU",
		"ID szablonu wydruku; bez niego API odrzuca dokument", SzablonyDomyslne)
	c.IDNumeracji = wczytajMapeRodzajow(&z, "SYSTIM_ID_NUMERACJI",
		"ID serii numeracji; bez niej API odrzuca dokument", NumeracjeDomyslne)

	if s := os.Getenv("SYSTIM_KONTO"); s != "" && strings.Contains(s, ".") {
		z.dodaj("SYSTIM_KONTO = %q wygląda na pełny adres; podaj samą poddomenę (np. abcd, nie abcd.systim.pl)", s)
	}

	c.DomyslnaFormaPlatnosci = wczytajFormePlatnosci(&z)
	c.VatIDs = wczytajVatIDs(&z)
	c.Timeout = wczytajCzas(&z, "SYSTIM_TIMEOUT", DomyslnyTimeout)

	// --- serwer ---
	c.Transport = wczytajTransport(&z)
	c.Adres = zDomyslna("SYSTIM_ADDR", DomyslnyAdres)
	c.KatalogPDF = zDomyslna("SYSTIM_KATALOG_PDF", DomyslnyKatalogPDF)
	if !filepath.IsAbs(c.KatalogPDF) {
		abs, err := filepath.Abs(c.KatalogPDF)
		if err == nil {
			c.KatalogPDF = abs
		}
	}
	c.LogLevel = wczytajPoziomLogow(&z)

	// --- bezpieczeństwo ---
	c.AuthDisabled = wczytajBool(&z, "SYSTIM_AUTH_DISABLED", false)
	c.SzkicKlucz = wczytajKlucz(&z)
	c.OIDCIssuer = strings.TrimSpace(os.Getenv("OIDC_ISSUER"))
	c.OIDCAudience = strings.TrimSpace(os.Getenv("OIDC_AUDIENCE"))
	c.OIDCScope = strings.TrimSpace(os.Getenv("OIDC_SCOPE"))
	c.OIDCScopesZadane = wczytajScopesZadane(c.OIDCScope)
	c.PublicURL = strings.TrimRight(strings.TrimSpace(os.Getenv("SYSTIM_PUBLIC_URL")), "/")
	c.DodatkoweOrig = wczytajListe("SYSTIM_DODATKOWE_ORIGINY")

	// --- limity ---
	c.MaxPozycji = wczytajInt(&z, "SYSTIM_MAX_POZYCJI", DomyslnyMaxPozycji)
	c.MaxCialo = int64(wczytajInt(&z, "SYSTIM_MAX_CIALO", int(DomyslnyMaxCialo)))
	c.WylaczOchroneLocalhost = wczytajBool(&z, "SYSTIM_WYLACZ_OCHRONE_LOCALHOST", false)

	// Wymagania zależne od transportu sprawdzamy dopiero po odczytaniu wszystkiego.
	if c.Transport == TransportHTTP {
		sprawdzWymaganiaHTTP(&z, c)
	}

	if err := z.blad(); err != nil {
		return nil, err
	}
	return c, nil
}

// sprawdzWymaganiaHTTP waliduje ustawienia potrzebne wyłącznie w trybie zdalnym.
func sprawdzWymaganiaHTTP(z *zbieraczBledow, c *Config) {
	if c.PublicURL == "" {
		z.dodaj("SYSTIM_PUBLIC_URL jest wymagany przy SYSTIM_TRANSPORT=http — " +
			"publiczny adres serwera, np. https://mcp.firma.pl. " +
			"Jest potrzebny do Protected Resource Metadata i do walidacji nagłówka Origin")
	} else {
		u, err := url.Parse(c.PublicURL)
		switch {
		case err != nil:
			z.dodaj("SYSTIM_PUBLIC_URL = %q nie jest poprawnym adresem: %v", c.PublicURL, err)
		case u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1":
			z.dodaj("SYSTIM_PUBLIC_URL = %q używa schematu %q; Claude łączy się z chmury Anthropic "+
				"i wymaga HTTPS (wyjątek: localhost przy testach lokalnych)", c.PublicURL, u.Scheme)
		case u.Host == "":
			z.dodaj("SYSTIM_PUBLIC_URL = %q nie zawiera nazwy hosta", c.PublicURL)
		}
	}

	if c.AuthDisabled {
		// Świadoma decyzja operatora; głośne ostrzeżenie wypisuje main przy każdym starcie.
		return
	}
	if c.OIDCIssuer == "" {
		z.dodaj("OIDC_ISSUER jest wymagany — adres serwera autoryzacji, " +
			"np. https://auth.firma.pl/realms/firma. Do testów lokalnych można wyłączyć " +
			"walidację przez SYSTIM_AUTH_DISABLED=true")
	} else if u, err := url.Parse(c.OIDCIssuer); err != nil || u.Scheme == "" || u.Host == "" {
		z.dodaj("OIDC_ISSUER = %q nie jest poprawnym adresem URL", c.OIDCIssuer)
	}
	if c.OIDCAudience == "" {
		z.dodaj("OIDC_AUDIENCE jest wymagany — oczekiwana wartość aud w tokenie dostępowym")
	}
	if c.OIDCScope == "" {
		z.dodaj("OIDC_SCOPE jest wymagany — scope wymagany do wywołania narzędzi, np. systim:faktury")
	}
}

// wymagane odczytuje zmienną, zgłaszając błąd wraz z opisem, gdy jej brakuje.
func wymagane(z *zbieraczBledow, nazwa, opis string) string {
	v := strings.TrimSpace(os.Getenv(nazwa))
	if v == "" {
		z.dodaj("brak wymaganej zmiennej %s (%s)", nazwa, opis)
	}
	return v
}

func zDomyslna(nazwa, domyslna string) string {
	if v := strings.TrimSpace(os.Getenv(nazwa)); v != "" {
		return v
	}
	return domyslna
}

// wczytajVatIDs parsuje mapowanie procent → ID stawki z SYSTIM_VAT_IDS.
func wczytajVatIDs(z *zbieraczBledow) map[string]int {
	raw := strings.TrimSpace(os.Getenv("SYSTIM_VAT_IDS"))
	if raw == "" {
		z.dodaj(`brak wymaganej zmiennej SYSTIM_VAT_IDS (JSON: mapa stawka → ID stawki w Systim, ` +
			`np. {"23":1,"8":2,"5":3,"0":4,"zw":5}). ` +
			`ID odczytasz narzędziem lista_stawek_vat po pierwszym uruchomieniu`)
		return nil
	}
	// Wartości bywają wpisywane jako stringi ("23":"1") — przyjmujemy oba warianty.
	var luzne map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &luzne); err != nil {
		z.dodaj("SYSTIM_VAT_IDS nie jest poprawnym JSON-em: %v", err)
		return nil
	}
	m := make(map[string]int, len(luzne))
	for k, v := range luzne {
		s := strings.Trim(strings.TrimSpace(string(v)), `"`)
		id, err := strconv.Atoi(s)
		if err != nil {
			z.dodaj("SYSTIM_VAT_IDS: stawka %q ma wartość %q, która nie jest liczbą całkowitą", k, s)
			continue
		}
		m[k] = id
	}
	if len(m) == 0 && len(luzne) == 0 {
		z.dodaj("SYSTIM_VAT_IDS nie zawiera żadnej stawki")
	}
	return m
}

func wczytajTransport(z *zbieraczBledow) Transport {
	v := strings.ToLower(zDomyslna("SYSTIM_TRANSPORT", string(TransportHTTP)))
	switch Transport(v) {
	case TransportHTTP, TransportStdio:
		return Transport(v)
	default:
		z.dodaj("SYSTIM_TRANSPORT = %q; dozwolone wartości to http albo stdio", v)
		return TransportHTTP
	}
}

func wczytajPoziomLogow(z *zbieraczBledow) slog.Level {
	v := strings.ToLower(zDomyslna("LOG_LEVEL", DomyslnyLogLevel))
	switch v {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		z.dodaj("LOG_LEVEL = %q; dozwolone wartości to debug, info, warn, error", v)
		return slog.LevelInfo
	}
}

func wczytajCzas(z *zbieraczBledow, nazwa string, domyslny time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(nazwa))
	if raw == "" {
		return domyslny
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		z.dodaj("%s = %q nie jest poprawnym czasem (oczekuję np. 30s, 1m)", nazwa, raw)
		return domyslny
	}
	if d <= 0 {
		z.dodaj("%s = %q musi być dodatnie", nazwa, raw)
		return domyslny
	}
	return d
}

func wczytajInt(z *zbieraczBledow, nazwa string, domyslny int) int {
	raw := strings.TrimSpace(os.Getenv(nazwa))
	if raw == "" {
		return domyslny
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		z.dodaj("%s = %q nie jest liczbą całkowitą", nazwa, raw)
		return domyslny
	}
	if n <= 0 {
		z.dodaj("%s = %d musi być większe od zera", nazwa, n)
		return domyslny
	}
	return n
}

func wczytajBool(z *zbieraczBledow, nazwa string, domyslna bool) bool {
	raw := strings.TrimSpace(os.Getenv(nazwa))
	if raw == "" {
		return domyslna
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		z.dodaj("%s = %q nie jest wartością logiczną (użyj true albo false)", nazwa, raw)
		return domyslna
	}
	return v
}

// wczytajKlucz odczytuje klucz HMAC do podpisywania szkiców.
func wczytajKlucz(z *zbieraczBledow) []byte {
	raw := os.Getenv("SYSTIM_SZKIC_KLUCZ")
	if strings.TrimSpace(raw) == "" {
		z.dodaj("brak wymaganej zmiennej SYSTIM_SZKIC_KLUCZ (klucz HMAC do podpisywania szkiców " +
			"faktur, minimum 32 bajty). Wygeneruj go poleceniem: openssl rand -base64 48")
		return nil
	}
	if len(raw) < 32 {
		z.dodaj("SYSTIM_SZKIC_KLUCZ ma %d bajtów, wymagane minimum to 32. "+
			"Wygeneruj dłuższy: openssl rand -base64 48", len(raw))
		return nil
	}
	return []byte(raw)
}

// NumeracjeDomyslne mapuje rodzaj dokumentu na ID serii numeracji ze standardowej
// kartoteki Systim (panel → Ustawienia → Numeracja dokumentów).
//
// Te ID identyfikują typ dokumentu, a nie konkretną serię klienta — symbol i wzór
// numeracji da się w panelu zmieniać, ale ID typu pozostaje. Wartości potwierdzone
// na eksporcie z zakładki „Numeracja dokumentów".
var NumeracjeDomyslne = map[int]int{
	0:  1,  // faktura VAT          → Faktura VAT (FV)
	1:  5,  // pro forma            → Faktura Pro Forma (PF)
	6:  39, // paragon fiskalny     → Paragon fiskalny (PFA)
	15: 9,  // paragon niefiskalny  → Paragon (PA)
	22: 16, // rachunek             → Faktura zw. z VAT, d. Rachunek (FA)
	26: 21, // oferta               → Oferta (OF)
}

// SzablonyDomyslne mapuje rodzaj dokumentu na ID szablonu wydruku.
//
// Szablon, tak samo jak numeracja, jest w Systim przypisany do typu dokumentu:
// szablon faktury VAT nie nadaje się do pro formy. Wskazane są warianty polskie —
// konto może mieć równolegle wersje EN i DE tego samego dokumentu, wtedy nadpisz
// je zmienną SYSTIM_ID_SZABLONU.
//
// Wartości potwierdzone na eksporcie z zakładki „Szablony wydruku".
var SzablonyDomyslne = map[int]int{
	0:  43, // faktura VAT          → Szablon faktury VAT
	1:  1,  // pro forma            → Szablon faktur Pro Forma
	6:  15, // paragon fiskalny     → Szablon paragonu
	15: 15, // paragon niefiskalny  → Szablon paragonu
	22: 22, // rachunek             → Szablon dokumentu Rachunek (RA)
	26: 26, // oferta               → Szablon oferty
}

// wczytajMapeRodzajow odczytuje zmienną wiążącą rodzaj dokumentu z ID zasobu
// w Systim (numeracji albo szablonu).
//
// Przyjmuje dwie postacie:
//   - pojedyncza liczba, np. 43 — użyta dla wszystkich rodzajów dokumentów;
//     zachowane dla zgodności wstecznej, ale poprawne tylko wtedy, gdy wystawiasz
//     jeden typ dokumentu;
//   - mapa JSON rodzaj → ID, np. {"0":43,"1":1} — brakujące rodzaje uzupełniane
//     wartościami domyślnymi.
func wczytajMapeRodzajow(z *zbieraczBledow, nazwa, opis string, domyslne map[int]int) map[int]string {
	raw := strings.TrimSpace(os.Getenv(nazwa))
	if raw == "" {
		z.dodaj("brak wymaganej zmiennej %s (%s). Zalecana postać to mapa rodzaj → ID, "+
			"np. %s — w Systim ten zasób jest przypisany do typu dokumentu",
			nazwa, opis, przykladMapy(domyslne))
		return nil
	}

	out := make(map[int]string, len(domyslne))

	// Postać skrócona: jedna liczba dla wszystkich rodzajów.
	if id, err := strconv.Atoi(raw); err == nil {
		if id <= 0 {
			z.dodaj("%s = %d musi być liczbą dodatnią", nazwa, id)
			return nil
		}
		for rodzaj := range domyslne {
			out[rodzaj] = strconv.Itoa(id)
		}
		return out
	}

	var luzne map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &luzne); err != nil {
		z.dodaj("%s = %q nie jest ani liczbą, ani poprawnym JSON-em. Oczekuję np. %s: %v",
			nazwa, raw, przykladMapy(domyslne), err)
		return nil
	}

	// Najpierw wartości domyślne, potem nadpisania z konfiguracji.
	for rodzaj, id := range domyslne {
		out[rodzaj] = strconv.Itoa(id)
	}
	for k, v := range luzne {
		rodzaj, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil {
			z.dodaj("%s: klucz %q nie jest numerem rodzaju dokumentu", nazwa, k)
			continue
		}
		s := strings.Trim(strings.TrimSpace(string(v)), `"`)
		id, err := strconv.Atoi(s)
		if err != nil || id <= 0 {
			z.dodaj("%s: rodzaj %d ma wartość %q, która nie jest dodatnią liczbą całkowitą", nazwa, rodzaj, s)
			continue
		}
		out[rodzaj] = strconv.Itoa(id)
	}
	return out
}

// przykladMapy buduje czytelny przykład na podstawie wartości domyślnych.
func przykladMapy(domyslne map[int]int) string {
	rodzaje := make([]int, 0, len(domyslne))
	for r := range domyslne {
		rodzaje = append(rodzaje, r)
	}
	sort.Ints(rodzaje)
	czesci := make([]string, 0, 2)
	for _, r := range rodzaje[:min(2, len(rodzaje))] {
		czesci = append(czesci, fmt.Sprintf("%q:%d", strconv.Itoa(r), domyslne[r]))
	}
	return "{" + strings.Join(czesci, ",") + "}"
}

// FormyPlatnosci to wartości akceptowane przez pole forma_platnosci w API Systim.
var FormyPlatnosci = []string{
	"przelew", "gotówka", "barter", "za pobraniem", "rozliczenie saldami", "karta płatnicza",
}

// wczytajFormePlatnosci odczytuje domyślną formę płatności.
//
// Pole jest opcjonalne, ale warto je ustawić: pominięcie formy płatności przy
// wystawianiu dokumentu oznacza wartość domyślną Systim, czyli gotówkę, co przy
// firmie rozliczającej się przelewem daje cicho błędny dokument.
func wczytajFormePlatnosci(z *zbieraczBledow) string {
	v := strings.TrimSpace(os.Getenv("SYSTIM_DOMYSLNA_FORMA_PLATNOSCI"))
	if v == "" {
		return ""
	}
	for _, d := range FormyPlatnosci {
		if strings.EqualFold(v, d) {
			return d
		}
	}
	z.dodaj("SYSTIM_DOMYSLNA_FORMA_PLATNOSCI = %q nie jest obsługiwaną formą płatności; dozwolone: %s",
		v, strings.Join(FormyPlatnosci, ", "))
	return ""
}

// DomyslneScopesZadane to scope'y doklejane do OIDC_SCOPE przy ogłaszaniu ich
// klientowi.
//
// offline_access jest tu nie bez powodu: authentik od wersji 2024.2 wydaje refresh
// token tylko wtedy, gdy klient wprost poprosił o ten scope. Bez refresh tokenu
// konektor Claude przestanie działać, gdy wygaśnie token dostępowy, a objawi się
// to jako „konektor nagle się rozłączył" po kilkunastu minutach.
var DomyslneScopesZadane = []string{"openid", "offline_access"}

// wczytajScopesZadane ustala listę scope'ów ogłaszanych klientowi.
//
// OIDC_SCOPES_REQUESTED nadpisuje domyślną listę w całości; wymagany scope
// jest do niej zawsze dopisywany, bo bez niego żaden token nie przejdzie walidacji.
func wczytajScopesZadane(wymagany string) []string {
	zrodlo := DomyslneScopesZadane
	if raw := strings.TrimSpace(os.Getenv("OIDC_SCOPES_REQUESTED")); raw != "" {
		zrodlo = strings.Fields(strings.ReplaceAll(raw, ",", " "))
	}

	out := make([]string, 0, len(zrodlo)+1)
	widziane := map[string]bool{}
	dodaj := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !widziane[s] {
			widziane[s] = true
			out = append(out, s)
		}
	}
	// Wymagany scope idzie pierwszy — to on jest właściwym powodem tej autoryzacji.
	dodaj(wymagany)
	for _, s := range zrodlo {
		dodaj(s)
	}
	return out
}

// wczytajListe odczytuje listę wartości rozdzielonych przecinkami.
func wczytajListe(nazwa string) []string {
	raw := strings.TrimSpace(os.Getenv(nazwa))
	if raw == "" {
		return nil
	}
	czesci := strings.Split(raw, ",")
	out := make([]string, 0, len(czesci))
	for _, c := range czesci {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, strings.TrimRight(c, "/"))
		}
	}
	sort.Strings(out)
	return out
}

// Numeracja zwraca ID serii numeracji właściwej dla danego rodzaju dokumentu.
//
// Brak wpisu jest błędem konfiguracji, a nie sytuacją do zignorowania: wysłanie
// numeracji od innego typu dokumentu kończy się odrzuceniem przez API.
func (c *Config) Numeracja(rodzaj int) (string, error) {
	if id, ok := c.IDNumeracji[rodzaj]; ok && id != "" {
		return id, nil
	}
	return "", fmt.Errorf(
		"brak ID numeracji dla rodzaju dokumentu %d. W Systim numeracja jest przypisana "+
			"do typu dokumentu, więc każdy wystawiany rodzaj musi mieć własny wpis. "+
			"Uzupełnij SYSTIM_ID_NUMERACJI, np. {\"0\":1,\"1\":5}; ID odczytasz w panelu: "+
			"Ustawienia → Numeracja dokumentów", rodzaj)
}

// Szablon zwraca ID szablonu wydruku właściwego dla danego rodzaju dokumentu.
//
// Szablon faktury VAT nie nadaje się do pro formy — Systim wiąże go z typem
// dokumentu dokładnie tak samo jak numerację.
func (c *Config) Szablon(rodzaj int) (string, error) {
	if id, ok := c.IDSzablonu[rodzaj]; ok && id != "" {
		return id, nil
	}
	return "", fmt.Errorf(
		"brak ID szablonu dla rodzaju dokumentu %d. W Systim szablon wydruku jest "+
			"przypisany do typu dokumentu, więc każdy wystawiany rodzaj musi mieć własny "+
			"wpis. Uzupełnij SYSTIM_ID_SZABLONU, np. {\"0\":43,\"1\":1}; ID odczytasz "+
			"w panelu: Ustawienia → Szablony wydruku", rodzaj)
}

// URLSystim zwraca adres endpointu API dla skonfigurowanego konta.
func (c *Config) URLSystim() string {
	return fmt.Sprintf("https://%s.systim.pl/jsonAPI", c.Konto)
}

// URLMetadanychZasobu zwraca adres Protected Resource Metadata.
func (c *Config) URLMetadanychZasobu() string {
	return c.PublicURL + "/.well-known/oauth-protected-resource"
}

// URLZasobu zwraca kanoniczny identyfikator zasobu chronionego (endpoint MCP).
func (c *Config) URLZasobu() string {
	return c.PublicURL + "/mcp"
}

// DozwoloneOriginy zwraca listę adresów akceptowanych w nagłówku Origin.
func (c *Config) DozwoloneOriginy() []string {
	out := make([]string, 0, len(c.DodatkoweOrig)+1)
	if c.PublicURL != "" {
		out = append(out, c.PublicURL)
	}
	out = append(out, c.DodatkoweOrig...)
	return out
}
