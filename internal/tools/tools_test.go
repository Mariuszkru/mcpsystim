package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mkrukowski/systim-mcp/internal/config"
	"github.com/mkrukowski/systim-mcp/internal/invoicing"
	"github.com/mkrukowski/systim-mcp/internal/systim"
)

// atrapaSystim udaje endpoint /jsonAPI. Handler dostaje act i rozparsowane ciało.
type atrapaSystim struct {
	srv *httptest.Server
	// ostatnieCialo to ciało ostatniego żądania addSellInvoice.
	ostatnieCialo url.Values
}

func nowaAtrapa(t *testing.T, h func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter)) *atrapaSystim {
	t.Helper()
	a := &atrapaSystim{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ciało, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(ciało))
		if err != nil {
			t.Fatalf("parsowanie ciała: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		act := form.Get("act")
		if act == "login" {
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
			return
		}
		h(a, act, form, w)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

// serwerTestowy buduje warstwę narzędzi wpiętą w atrapę API.
func serwerDoTestow(t *testing.T, a *atrapaSystim) (*Serwer, *invoicing.PodpisSzkicow, string) {
	t.Helper()

	klient, err := systim.NewClient(systim.Opcje{
		Login: "u", Pass: "p", BaseURL: a.srv.URL, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient = %v", err)
	}
	stawki, err := invoicing.NoweStawkiVAT(map[string]int{"23": 1, "8": 2, "5": 3, "0": 4, "zw": 5})
	if err != nil {
		t.Fatalf("NoweStawkiVAT = %v", err)
	}
	szkice, err := invoicing.NowyPodpisSzkicow([]byte(strings.Repeat("k", 48)))
	if err != nil {
		t.Fatalf("NowyPodpisSzkicow = %v", err)
	}
	katalog := t.TempDir()
	// FormyPlatnosciIDs jak w produkcji — do API idzie ID, nie nazwa.
	formy := make(map[string]string, len(systim.IDFormyPlatnosci))
	for nazwa, id := range systim.IDFormyPlatnosci {
		formy[nazwa] = strconv.Itoa(id)
	}
	cfg := &config.Config{
		IDSzablonu:        map[int]string{0: "43", 1: "1"},
		IDNumeracji:       map[int]string{0: "1", 1: "5"},
		FormyPlatnosciIDs: formy,
		KatalogPDF:        katalog,
		MaxPozycji:        config.DomyslnyMaxPozycji,
	}
	return NowySerwer(klient, stawki, szkice, cfg, nil), szkice, katalog
}

// polaczonyKlient uruchamia serwer MCP na transporcie in-memory i zwraca sesję klienta.
func polaczonyKlient(t *testing.T, s *Serwer) *mcp.ClientSession {
	t.Helper()
	srv := s.Zarejestruj(&mcp.Implementation{Name: "systim-mcp", Version: "test"}, nil)

	tSerwer, tKlient := mcp.NewInMemoryTransports()
	ctx := context.Background()
	sesjaSerwera, err := srv.Connect(ctx, tSerwer, nil)
	if err != nil {
		t.Fatalf("Connect serwera = %v", err)
	}
	t.Cleanup(func() { sesjaSerwera.Close() })

	klient := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	sesja, err := klient.Connect(ctx, tKlient, nil)
	if err != nil {
		t.Fatalf("Connect klienta = %v", err)
	}
	t.Cleanup(func() { sesja.Close() })
	return sesja
}

// tekstWyniku skleja treść tekstową odpowiedzi narzędzia.
func tekstWyniku(t *testing.T, w *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range w.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// strukturaWyniku dekoduje ustrukturyzowaną odpowiedź narzędzia.
func strukturaWyniku[T any](t *testing.T, w *mcp.CallToolResult) T {
	t.Helper()
	var v T
	dane, err := json.Marshal(w.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal StructuredContent = %v", err)
	}
	if err := json.Unmarshal(dane, &v); err != nil {
		t.Fatalf("Unmarshal StructuredContent = %v (dane: %s)", err, dane)
	}
	return v
}

func TestWszystkieNarzedziaSaZarejestrowaneZeSchematami(t *testing.T) {
	// Ten test wyłapuje też błędy generowania schematu ze struktur Go —
	// AddTool odrzuciłby typ, którego nie umie opisać.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools = %v", err)
	}

	chciane := map[string]struct {
		readOnly     bool
		destrukcyjne bool
	}{
		"lista_stawek_vat":   {readOnly: true},
		"szukaj_kontrahenta": {readOnly: true},
		"szukaj_produktu":    {readOnly: true},
		"przygotuj_fakture":  {readOnly: true},
		"zatwierdz_fakture":  {readOnly: false, destrukcyjne: true},
		"lista_faktur":       {readOnly: true},
		"pobierz_pdf":        {readOnly: false},
	}
	znalezione := map[string]*mcp.Tool{}
	for _, n := range wynik.Tools {
		znalezione[n.Name] = n
	}
	if len(znalezione) != len(chciane) {
		t.Errorf("zarejestrowano %d narzędzi, chcę %d: %v", len(znalezione), len(chciane), znalezione)
	}

	for nazwa, ocz := range chciane {
		n, ok := znalezione[nazwa]
		if !ok {
			t.Errorf("brak narzędzia %q", nazwa)
			continue
		}
		if n.Description == "" {
			t.Errorf("%s: brak opisu", nazwa)
		}
		if n.InputSchema == nil {
			t.Errorf("%s: brak schematu wejścia", nazwa)
		}
		if n.Annotations == nil {
			t.Fatalf("%s: brak adnotacji", nazwa)
		}
		if n.Annotations.ReadOnlyHint != ocz.readOnly {
			t.Errorf("%s: ReadOnlyHint = %v, chcę %v", nazwa, n.Annotations.ReadOnlyHint, ocz.readOnly)
		}
		if ocz.destrukcyjne {
			if n.Annotations.DestructiveHint == nil || !*n.Annotations.DestructiveHint {
				t.Errorf("%s: DestructiveHint musi być true", nazwa)
			}
			if n.Annotations.IdempotentHint {
				t.Errorf("%s: IdempotentHint musi być false — każde wywołanie tworzy nowy dokument", nazwa)
			}
		}
	}

	// Opis zatwierdz_fakture musi ostrzegać, że operacja jest nieodwracalna.
	opis := znalezione["zatwierdz_fakture"].Description
	for _, fragment := range []string{"NIEODWRACALNA", "zgodził"} {
		if !strings.Contains(opis, fragment) {
			t.Errorf("opis zatwierdz_fakture nie zawiera %q:\n%s", fragment, opis)
		}
	}
}

func TestSzukajKontrahentaPoNIPzMyslnikami(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{
			"41":{"nazwa":"Kowalski &amp; Synowie sp. z o.o.","nip":"123-456-78-90","miasto":"Warszawa","kod_pocztowy":"00-001"},
			"42":{"nazwa":"Inna Firma","nip":"9876543210"}
		}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	// Wszystkie te zapisy muszą trafić w ten sam rekord.
	for _, fraza := range []string{"1234567890", "123-456-78-90", "123 456 78 90"} {
		wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "szukaj_kontrahenta",
			Arguments: map[string]any{"fraza": fraza},
		})
		if err != nil {
			t.Fatalf("CallTool(%q) = %v", fraza, err)
		}
		if wynik.IsError {
			t.Fatalf("CallTool(%q) zwróciło błąd: %s", fraza, tekstWyniku(t, wynik))
		}
		wy := strukturaWyniku[WyjscieKontrahenci](t, wynik)
		if len(wy.Kontrahenci) != 1 {
			t.Fatalf("fraza %q: znaleziono %d kontrahentów, chcę 1", fraza, len(wy.Kontrahenci))
		}
		if wy.Kontrahenci[0].ID != "41" {
			t.Errorf("fraza %q: ID = %q, chcę 41", fraza, wy.Kontrahenci[0].ID)
		}
		// Encje HTML muszą być odkodowane, zanim trafią do modelu.
		if wy.Kontrahenci[0].Nazwa != "Kowalski & Synowie sp. z o.o." {
			t.Errorf("fraza %q: nazwa = %q", fraza, wy.Kontrahenci[0].Nazwa)
		}
	}
}

func TestSzukajKontrahentaIgnorujeWielkoscLiter(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"PIEKARNIA Złoty Kłos","nip":"1112223344"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	for _, fraza := range []string{"piekarnia", "PIEKARNIA", "Złoty", "złoty kłos"} {
		wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "szukaj_kontrahenta",
			Arguments: map[string]any{"fraza": fraza},
		})
		if err != nil {
			t.Fatalf("CallTool(%q) = %v", fraza, err)
		}
		wy := strukturaWyniku[WyjscieKontrahenci](t, wynik)
		if len(wy.Kontrahenci) != 1 {
			t.Errorf("fraza %q: znaleziono %d, chcę 1", fraza, len(wy.Kontrahenci))
		}
	}
}

func TestSzukajKontrahentaLimit25Wynikow(t *testing.T) {
	// Kartoteka z 40 pasującymi rekordami — do modelu ma trafić najwyżej 25.
	var b strings.Builder
	b.WriteString(`{"error":{"code":0,"message":""},"result":{`)
	for i := range 40 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"`)
		b.WriteString(string(rune('0' + i/10)))
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString(`":{"nazwa":"Firma Testowa nr `)
		b.WriteString(string(rune('0' + i/10)))
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString(`"}`)
	}
	b.WriteString(`}}`)
	odpowiedz := b.String()

	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, odpowiedz)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "szukaj_kontrahenta",
		Arguments: map[string]any{"fraza": "Firma Testowa"},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	wy := strukturaWyniku[WyjscieKontrahenci](t, wynik)
	if len(wy.Kontrahenci) != MaxWynikowWyszukiwania {
		t.Errorf("zwrócono %d wyników, chcę %d", len(wy.Kontrahenci), MaxWynikowWyszukiwania)
	}
	if !wy.Przyciete {
		t.Error("Przyciete = false, chcę informacji o przycięciu listy")
	}
	if wy.Znaleziono != 40 {
		t.Errorf("Znaleziono = %d, chcę 40", wy.Znaleziono)
	}
}

func TestPrzygotujFaktureNiczegoNieZapisuje(t *testing.T) {
	var wywolaneMetody []string
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		wywolaneMetody = append(wywolaneMetody, act)
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-24",
			"pozycje": []map[string]any{
				{"opis": "Konsultacje", "ilosc": "10", "cena_netto": "200", "stawka_vat": "23", "jednostka": "h"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("narzędzie zwróciło błąd: %s", tekstWyniku(t, wynik))
	}

	for _, m := range wywolaneMetody {
		if m == "addSellInvoice" {
			t.Fatal("przygotuj_fakture wywołało addSellInvoice — podgląd nie może niczego zapisywać")
		}
	}

	wy := strukturaWyniku[WyjsciePrzygotuj](t, wynik)
	if wy.SzkicID == "" {
		t.Fatal("brak szkic_id")
	}
	if wy.RazemNetto != "2000.00" || wy.RazemVAT != "460.00" || wy.RazemBrutto != "2460.00" {
		t.Errorf("sumy: netto=%s VAT=%s brutto=%s, chcę 2000.00 / 460.00 / 2460.00",
			wy.RazemNetto, wy.RazemVAT, wy.RazemBrutto)
	}
	if wy.NazwaKontrahenta != "Alfa" {
		t.Errorf("nazwa kontrahenta = %q, chcę Alfa", wy.NazwaKontrahenta)
	}
	// Podgląd tekstowy musi jasno mówić, że nic jeszcze nie powstało.
	tresc := tekstWyniku(t, wynik)
	if !strings.Contains(tresc, "nic jeszcze nie zostało zapisane") {
		t.Errorf("podgląd nie mówi wprost, że niczego nie zapisano:\n%s", tresc)
	}
}

func TestPrzygotujFaktureStawkaBezMapowania(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-24",
			"pozycje": []map[string]any{
				{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "7"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("chcę błędu dla stawki VAT bez mapowania")
	}
	tresc := tekstWyniku(t, wynik)
	for _, fragment := range []string{"SYSTIM_VAT_IDS", "lista_stawek_vat"} {
		if !strings.Contains(tresc, fragment) {
			t.Errorf("komunikat nie zawiera %q:\n%s", fragment, tresc)
		}
	}
}

func TestPrzygotujFaktureOdrzucaWaluteObca(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-24",
			"rodzaj":           23,
			"pozycje": []map[string]any{
				{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("rodzaj 23 (faktura w walucie) został przyjęty")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "walut obcych nie jest") {
		t.Errorf("komunikat nie mówi o braku obsługi walut obcych:\n%s", tekstWyniku(t, wynik))
	}
}

func TestPrzygotujFaktureZlyFormatDaty(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	for _, data := range []string{"24.07.2026", "24/07/2026", "2026-13-01", "jutro"} {
		wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "przygotuj_fakture",
			Arguments: map[string]any{
				"id_kontrahenta":   "41",
				"data_wystawienia": data,
				"pozycje": []map[string]any{
					{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
				},
			},
		})
		if err != nil {
			t.Fatalf("CallTool = %v", err)
		}
		if !wynik.IsError {
			t.Errorf("data %q została przyjęta", data)
			continue
		}
		if !strings.Contains(tekstWyniku(t, wynik), "RRRR-MM-DD") {
			t.Errorf("data %q: komunikat nie podaje oczekiwanego formatu:\n%s", data, tekstWyniku(t, wynik))
		}
	}
}

func TestZatwierdzFaktureWysylaTabliceIczytaResultCode(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"1001","numer":"FV\/12\/2026","result_code":102}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-24",
			"termin_platnosci": 14,
			"forma_platnosci":  "przelew",
			"pozycje": []map[string]any{
				{"opis": "Konsultacje", "ilosc": "10", "cena_netto": "200", "stawka_vat": "23", "jednostka": "h"},
			},
		},
	})
	if err != nil {
		t.Fatalf("przygotuj_fakture = %v", err)
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil {
		t.Fatalf("zatwierdz_fakture = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("zatwierdz_fakture zwróciło błąd: %s", tekstWyniku(t, wynik))
	}

	// Do API poszły równoległe tablice PHP, a nie JSON.
	if got := a.ostatnieCialo.Get("opis[0]"); got != "Konsultacje" {
		t.Errorf("opis[0] = %q, chcę Konsultacje", got)
	}
	if got := a.ostatnieCialo.Get("kwota_netto[0]"); got != "2000.00" {
		t.Errorf("kwota_netto[0] = %q, chcę 2000.00", got)
	}
	// stawka_vat to ID stawki w Systim, nie procent.
	if got := a.ostatnieCialo.Get("stawka_vat[0]"); got != "1" {
		t.Errorf("stawka_vat[0] = %q, chcę 1 (ID stawki, nie procent 23)", got)
	}
	// id_szablonu i id_numeracji muszą być obecne — bez nich API odrzuca dokument.
	// Numeracja jest dobrana do rodzaju dokumentu; tu rodzaj to 0 (faktura VAT).
	if a.ostatnieCialo.Get("id_szablonu") != "43" || a.ostatnieCialo.Get("id_numeracji") != "1" {
		t.Errorf("id_szablonu = %q, id_numeracji = %q, chcę 43 i 1",
			a.ostatnieCialo.Get("id_szablonu"), a.ostatnieCialo.Get("id_numeracji"))
	}
	if a.ostatnieCialo.Get("termin_platnosci") != "14" {
		t.Errorf("termin_platnosci = %q, chcę 14", a.ostatnieCialo.Get("termin_platnosci"))
	}

	wy := strukturaWyniku[WyjscieZatwierdz](t, wynik)
	if wy.IDFaktury != "1001" || wy.Numer != "FV/12/2026" {
		t.Errorf("wynik = %+v", wy)
	}
	// result_code 102 to sytuacja, o której użytkownik musi się dowiedzieć.
	if !wy.WymagaUwagi {
		t.Error("WymagaUwagi = false dla result_code 102")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "NIE powiodło") {
		t.Errorf("odpowiedź nie ostrzega o nieudanym księgowaniu:\n%s", tekstWyniku(t, wynik))
	}
}

func TestZatwierdzFaktureWygaslySzkic(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		if act == "addSellInvoice" {
			t.Error("wygasły szkic nie może dotrzeć do addSellInvoice")
		}
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
	})
	s, szkice, _ := serwerDoTestow(t, a)

	// Szkic sprzed 31 minut, przy TTL 30 minut.
	stary := time.Now().Add(-31 * time.Minute)
	szkicID := podpiszWCzasie(t, szkice, stary)

	sesja := polaczonyKlient(t, s)
	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkicID},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("wygasły szkic został przyjęty")
	}
	tresc := tekstWyniku(t, wynik)
	if !strings.Contains(tresc, "wygasł") || !strings.Contains(tresc, "przygotuj_fakture") {
		t.Errorf("komunikat o wygasłym szkicu jest nieprzydatny:\n%s", tresc)
	}
}

// podpiszWCzasie tworzy szkic z przesuniętym czasem wystawienia.
func podpiszWCzasie(t *testing.T, p *invoicing.PodpisSzkicow, kiedy time.Time) string {
	t.Helper()
	stawki, err := invoicing.NoweStawkiVAT(map[string]int{"23": 1})
	if err != nil {
		t.Fatalf("NoweStawkiVAT = %v", err)
	}
	pozycje, sumy, err := invoicing.Oblicz([]invoicing.PozycjaWejsciowa{
		{Opis: "Usługa", Ilosc: "1", CenaNetto: "100", StawkaVAT: "23"},
	}, "", stawki)
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}

	pomocniczy, err := invoicing.NowyPodpisSzkicow([]byte(strings.Repeat("k", 48)))
	if err != nil {
		t.Fatalf("NowyPodpisSzkicow = %v", err)
	}
	pomocniczy.UstawCzasDoTestow(func() time.Time { return kiedy })
	szkicID, err := pomocniczy.Podpisz(invoicing.Dokument{
		IDKontrahenta:   "41",
		DataWystawienia: "2026-07-24",
		DataSprzedazy:   "2026-07-24",
		Pozycje:         pozycje,
		Podsumowanie:    sumy,
	})
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}
	return szkicID
}

func TestZatwierdzFaktureSfalszowanySzkic(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		if act == "addSellInvoice" {
			t.Error("sfałszowany szkic nie może dotrzeć do addSellInvoice")
		}
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-24",
			"pozycje": []map[string]any{
				{"opis": "Konsultacje", "ilosc": "10", "cena_netto": "200", "stawka_vat": "23"},
			},
		},
	})
	if err != nil {
		t.Fatalf("przygotuj_fakture = %v", err)
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	// Podmieniamy kwotę w payloadzie, zostawiając oryginalny podpis.
	czescPayload, czescPodpis, _ := strings.Cut(szkic.SzkicID, ".")
	payload, err := base64.RawURLEncoding.DecodeString(czescPayload)
	if err != nil {
		t.Fatalf("dekodowanie payloadu = %v", err)
	}
	zmieniony := strings.Replace(string(payload), `"2000"`, `"1"`, 1)
	if zmieniony == string(payload) {
		t.Fatalf("nie udało się podmienić kwoty w: %s", payload)
	}
	sfalszowany := base64.RawURLEncoding.EncodeToString([]byte(zmieniony)) + "." + czescPodpis

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": sfalszowany},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("szkic ze zmienioną kwotą został przyjęty")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "podpis") {
		t.Errorf("komunikat nie wskazuje na niezgodny podpis:\n%s", tekstWyniku(t, wynik))
	}
}

func TestZatwierdzFaktureMiesiacZamkniety(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			io.WriteString(w, `{"error":{"code":16,"message":"Miesiac jest zamkniety"},"result":null}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, _ := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-01-15",
			"pozycje": []map[string]any{
				{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
			},
		},
	})
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("chcę błędu dla zamkniętego miesiąca")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "okres księgowy") {
		t.Errorf("komunikat nie tłumaczy, o co chodzi:\n%s", tekstWyniku(t, wynik))
	}
}

func TestPobierzPDFZapisujePlikIniezwracaBase64(t *testing.T) {
	// Zawartość udająca PDF.
	zawartosc := []byte("%PDF-1.4\nprzykładowa treść\n%%EOF")
	zakodowana := base64.StdEncoding.EncodeToString(zawartosc)

	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		if act != "getSellInvoicePDF" {
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
			return
		}
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"file":"`+zakodowana+`","name":"FV\/12\/2026.pdf"}}`)
	})
	s, _, katalog := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "pobierz_pdf",
		Arguments: map[string]any{"id_faktury": "1001"},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("narzędzie zwróciło błąd: %s", tekstWyniku(t, wynik))
	}

	wy := strukturaWyniku[WyjsciePDF](t, wynik)
	if wy.RozmiarBajt != len(zawartosc) {
		t.Errorf("rozmiar = %d, chcę %d", wy.RozmiarBajt, len(zawartosc))
	}
	// Ukośniki z numeru faktury nie mogą utworzyć podkatalogów ani wyjść z katalogu.
	if filepath.Dir(wy.Sciezka) != katalog {
		t.Errorf("plik zapisany w %q, chcę w %q", filepath.Dir(wy.Sciezka), katalog)
	}
	zapisane, err := os.ReadFile(wy.Sciezka)
	if err != nil {
		t.Fatalf("odczyt zapisanego pliku = %v", err)
	}
	if string(zapisane) != string(zawartosc) {
		t.Errorf("zapisana treść się nie zgadza")
	}

	// Base64 nie może trafić do odpowiedzi — zapchałoby kontekst.
	if strings.Contains(tekstWyniku(t, wynik), zakodowana) {
		t.Error("odpowiedź zawiera zawartość pliku w base64")
	}
	dane, _ := json.Marshal(wynik.StructuredContent)
	if strings.Contains(string(dane), zakodowana) {
		t.Error("ustrukturyzowana odpowiedź zawiera zawartość pliku w base64")
	}
}

func TestBezpiecznaNazwaPliku(t *testing.T) {
	przypadki := []struct {
		nazwa, id, chce string
	}{
		// Numer dokumentu z ukośnikami zostaje czytelny, ale nie tworzy podkatalogów.
		{"FV/12/2026.pdf", "1001", "FV_12_2026.pdf"},
		// Próby wyjścia z katalogu tracą wszystkie separatory.
		{"../../etc/passwd", "1001", "etc_passwd.pdf"},
		{"/etc/shadow", "1001", "etc_shadow.pdf"},
		{"....//....//tmp/x.pdf", "1001", "tmp_x.pdf"},
		{"", "1001", "faktura_1001.pdf"},
		{"..", "1001", "faktura_1001.pdf"},
		{"...", "1001", "faktura_1001.pdf"},
		{"faktura.pdf", "1001", "faktura.pdf"},
		{"Faktura Zaliczkowa.PDF", "1001", "Faktura_Zaliczkowa.pdf"},
		// Polskie znaki są transliterowane, a nie zamieniane na podkreślenia.
		{"żółć.pdf", "1001", "zolc.pdf"},
		{"Faktura Główna.pdf", "42", "Faktura_Glowna.pdf"},
		{"中文.pdf", "1001", "faktura_1001.pdf"},
	}
	for _, p := range przypadki {
		got := bezpiecznaNazwaPliku(p.nazwa, p.id)
		if got != p.chce {
			t.Errorf("bezpiecznaNazwaPliku(%q) = %q, chcę %q", p.nazwa, got, p.chce)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("bezpiecznaNazwaPliku(%q) = %q zawiera separator ścieżki", p.nazwa, got)
		}
	}
}

func TestListaFakturWalidujeZakresDat(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "lista_faktur",
		Arguments: map[string]any{"data_od": "2026-07-31", "data_do": "2026-07-01"},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("odwrócony zakres dat został przyjęty")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "późniejsza") {
		t.Errorf("komunikat = %s", tekstWyniku(t, wynik))
	}
}

func TestListaFakturPrzekazujeFiltrDat(t *testing.T) {
	var widzianeOd, widzianeDo string
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		if act == "listSellInvoices" {
			widzianeOd = form.Get("data_wystawienia_od")
			widzianeDo = form.Get("data_wystawienia_do")
		}
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"1001":{"numer":"FV\/12\/2026","data_wystawienia":"2026-07-24","kwota_brutto":"2460.00"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "lista_faktur",
		Arguments: map[string]any{"data_od": "2026-07-01", "data_do": "2026-07-31"},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if widzianeOd != "2026-07-01" || widzianeDo != "2026-07-31" {
		t.Errorf("filtr przekazany jako od=%q do=%q", widzianeOd, widzianeDo)
	}
	wy := strukturaWyniku[WyjscieListaFaktur](t, wynik)
	if wy.Liczba != 1 || wy.Faktury[0].Numer != "FV/12/2026" {
		t.Errorf("wynik = %+v", wy)
	}
}

func TestListaStawekVATPokazujeIDIkonfiguracje(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"1":{"nazwa":"23%"},"5":{"nazwa":"zw"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{Name: "lista_stawek_vat"})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("błąd: %s", tekstWyniku(t, wynik))
	}
	wy := strukturaWyniku[WyjscieStawki](t, wynik)
	if len(wy.Stawki) != 2 {
		t.Fatalf("stawek = %d, chcę 2", len(wy.Stawki))
	}
	if wy.Stawki[0].ID != "1" || wy.Stawki[0].Opis != "23%" {
		t.Errorf("stawka[0] = %+v", wy.Stawki[0])
	}
	if !strings.Contains(wy.Wskazowka, "SYSTIM_VAT_IDS") {
		t.Errorf("wskazówka nie mówi, co zrobić z ID: %q", wy.Wskazowka)
	}
}

func TestNarzedziaZglaszajaBladThrottlingu(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":2,"message":"Dostep zabroniony"},"result":null}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "szukaj_kontrahenta",
		Arguments: map[string]any{"fraza": "cokolwiek"},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("chcę błędu dla kodu 2")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "throttling") {
		t.Errorf("komunikat nie sugeruje throttlingu:\n%s", tekstWyniku(t, wynik))
	}
}

func TestNumeracjaDobieranaDoRodzajuDokumentu(t *testing.T) {
	// Regresja z produkcji: przy jednej numeracji dla wszystkich rodzajów Systim
	// odrzucał pro formę komunikatem „błędne przypisanie rodzaju dokumentu do numeracji".
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"7","numer":"PF 1\/07\/2026","result_code":0}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-25",
			"rodzaj":           1, // pro forma
			"pozycje": []map[string]any{
				{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
			},
		},
	})
	if err != nil {
		t.Fatalf("przygotuj_fakture = %v", err)
	}
	if przygotowanie.IsError {
		t.Fatalf("przygotuj_fakture zwróciło błąd: %s", tekstWyniku(t, przygotowanie))
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil {
		t.Fatalf("zatwierdz_fakture = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("zatwierdz_fakture zwróciło błąd: %s", tekstWyniku(t, wynik))
	}

	// Pro forma musi dostać numerację pro form (5), a nie faktury VAT (1).
	if got := a.ostatnieCialo.Get("id_numeracji"); got != "5" {
		t.Errorf("id_numeracji = %q, chcę 5 dla pro formy", got)
	}
	if got := a.ostatnieCialo.Get("rodzaj"); got != "1" {
		t.Errorf("rodzaj = %q, chcę 1", got)
	}
}

func TestBrakNumeracjiDlaRodzajuWykrytyJuzWPodgladzie(t *testing.T) {
	// Błąd konfiguracji ma wyjść przy przygotuj_fakture, a nie dopiero przy
	// nieodwracalnym zatwierdzeniu.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		if act == "addSellInvoice" {
			t.Error("dokument bez numeracji nie może dotrzeć do addSellInvoice")
		}
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	// Konfiguracja zna tylko fakturę VAT.
	s.cfg.IDNumeracji = map[int]string{0: "1"}
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-25",
			"rodzaj":           1,
			"pozycje": []map[string]any{
				{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if !wynik.IsError {
		t.Fatal("brak numeracji dla pro formy został przepuszczony")
	}
	if !strings.Contains(tekstWyniku(t, wynik), "SYSTIM_ID_NUMERACJI") {
		t.Errorf("komunikat nie podpowiada, co poprawić:\n%s", tekstWyniku(t, wynik))
	}
}

func TestSzablonDobieranyDoRodzajuPrzyZatwierdzeniu(t *testing.T) {
	// Regresja: pro forma wysyłana z szablonem faktury VAT zostałaby odrzucona
	// tak samo jak przy złej numeracji.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"9","numer":"PF 3\/07\/2026","result_code":0}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-25",
			"rodzaj":           1,
			"pozycje": []map[string]any{
				{"opis": "Usługa", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
			},
		},
	})
	if err != nil || przygotowanie.IsError {
		t.Fatalf("przygotuj_fakture = %v / %s", err, tekstWyniku(t, przygotowanie))
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("zatwierdz_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}

	// Pro forma: szablon 1 i numeracja 5, a nie szablon faktury VAT (43) i numeracja 1.
	if got := a.ostatnieCialo.Get("id_szablonu"); got != "1" {
		t.Errorf("id_szablonu = %q, chcę 1 dla pro formy", got)
	}
	if got := a.ostatnieCialo.Get("id_numeracji"); got != "5" {
		t.Errorf("id_numeracji = %q, chcę 5 dla pro formy", got)
	}
}

func TestNumerDoczytywanyGdyAPIGoNieZwroci(t *testing.T) {
	// Zaobserwowane na produkcji: addSellInvoice zwróciło id, ale puste "numer",
	// mimo że dokument numer dostał. Użytkownik musi go poznać.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"3","numer":"","result_code":0}}`)
		case "listSellInvoices":
			if form.Get("ids") != "3" {
				t.Errorf("ids = %q, chcę 3 — numer doczytujemy po ID wystawionego dokumentu", form.Get("ids"))
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"3":{"numer":"PF 3\/07\/2026"}}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, _ := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25", "rodzaj": 1,
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("zatwierdz_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}

	wy := strukturaWyniku[WyjscieZatwierdz](t, wynik)
	if wy.Numer != "PF 3/07/2026" {
		t.Errorf("Numer = %q, chcę PF 3/07/2026 doczytane po ID", wy.Numer)
	}
	if !strings.Contains(tekstWyniku(t, wynik), "PF 3/07/2026") {
		t.Errorf("numer nie trafił do odpowiedzi tekstowej:\n%s", tekstWyniku(t, wynik))
	}
}

func TestBrakNumeruNiePrzewracaWystawienia(t *testing.T) {
	// Dokument już istnieje — nieudane doczytanie numeru nie może zgłosić porażki.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"3","numer":"","result_code":0}}`)
		case "listSellInvoices":
			io.WriteString(w, `{"error":{"code":2,"message":"Dostep zabroniony"},"result":null}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, _ := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25", "rodzaj": 1,
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil {
		t.Fatalf("CallTool = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("wystawienie zgłoszone jako błąd mimo utworzonego dokumentu: %s", tekstWyniku(t, wynik))
	}
	wy := strukturaWyniku[WyjscieZatwierdz](t, wynik)
	if wy.IDFaktury != "3" {
		t.Errorf("IDFaktury = %q, chcę 3", wy.IDFaktury)
	}
	if !strings.Contains(tekstWyniku(t, wynik), "sprawdź dokument w panelu") {
		t.Errorf("odpowiedź nie tłumaczy braku numeru:\n%s", tekstWyniku(t, wynik))
	}
}

func TestDomyslnaFormaPlatnosciTrafiaNaDokument(t *testing.T) {
	// Regresja z żywego konta: dokumenty wystawione bez podanej formy płatności
	// wychodziły z gotówką, mimo że firma rozlicza się przelewem.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"1","numer":"FV 1\/07\/2026","result_code":100}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	s.cfg.DomyslnaFormaPlatnosci = "przelew"
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	// Pole celowo pominięte — ma zadziałać wartość z konfiguracji.
	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25",
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	if err != nil || przygotowanie.IsError {
		t.Fatalf("przygotuj_fakture = %v / %s", err, tekstWyniku(t, przygotowanie))
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)
	if szkic.FormaPlatnosci != "przelew" {
		t.Errorf("podgląd pokazuje formę %q, chcę przelew", szkic.FormaPlatnosci)
	}

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("zatwierdz_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}
	// Do API idzie ID formy płatności, nie jej nazwa.
	if got := a.ostatnieCialo.Get("forma_platnosci"); got != "1" {
		t.Errorf("forma_platnosci = %q, chcę 1 (ID przelewu)", got)
	}
}

func TestJawnaFormaPlatnosciNadpisujeDomyslna(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"1","numer":"FV 1\/07\/2026","result_code":100}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	s.cfg.DomyslnaFormaPlatnosci = "przelew"
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, _ := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25", "forma_platnosci": "gotówka",
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("zatwierdz_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}
	if got := a.ostatnieCialo.Get("forma_platnosci"); got != "2" {
		t.Errorf("forma_platnosci = %q, chcę 2 (ID gotówki) — jawna wartość ma wygrywać", got)
	}
}

func TestListaFakturPokazujeFormePlatnosci(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"8":{"numer":"FV 5\/07\/2026","kwota_brutto":"123.00","forma_platnosci":"przelew","termin_platnosci":"2026-08-01"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "lista_faktur",
		Arguments: map[string]any{"data_od": "2026-07-01", "data_do": "2026-07-31"},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("lista_faktur = %v / %s", err, tekstWyniku(t, wynik))
	}
	wy := strukturaWyniku[WyjscieListaFaktur](t, wynik)
	if len(wy.Faktury) != 1 {
		t.Fatalf("faktur = %d, chcę 1", len(wy.Faktury))
	}
	// Bez tego pola forma płatności była sprawdzalna wyłącznie w panelu.
	if wy.Faktury[0].FormaPlatnosci != "przelew" {
		t.Errorf("FormaPlatnosci = %q, chcę przelew", wy.Faktury[0].FormaPlatnosci)
	}
	if wy.Faktury[0].TerminPlatnosci != "2026-08-01" {
		t.Errorf("TerminPlatnosci = %q", wy.Faktury[0].TerminPlatnosci)
	}
	if !strings.Contains(tekstWyniku(t, wynik), "przelew") {
		t.Errorf("forma płatności nie trafiła do odpowiedzi tekstowej:\n%s", tekstWyniku(t, wynik))
	}
}

func TestFormaPlatnosciWysylanaJakoIDGdyWlaczone(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"1","numer":"FV 1\/07\/2026","result_code":100}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25", "forma_platnosci": "przelew",
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	if err != nil || przygotowanie.IsError {
		t.Fatalf("przygotuj_fakture = %v / %s", err, tekstWyniku(t, przygotowanie))
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)
	// Podgląd pokazuje nazwę — ID to szczegół transportu, nie treść dla użytkownika.
	if szkic.FormaPlatnosci != "przelew" {
		t.Errorf("podgląd pokazuje %q, chcę przelew", szkic.FormaPlatnosci)
	}

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("zatwierdz_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}
	if got := a.ostatnieCialo.Get("forma_platnosci"); got != "1" {
		t.Errorf("forma_platnosci = %q, chcę 1 (ID przelewu)", got)
	}
}

func TestBrakMapowaniaFormyPlatnosciOstrzegaINieWysylaPola(t *testing.T) {
	// Bez ID lepiej nie wysyłać nic niż wysłać nazwę, której API nie honoruje —
	// ale użytkownik musi wiedzieć, że forma nie trafi na dokument.
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "addSellInvoice":
			a.ostatnieCialo = form
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"1","numer":"FV 1\/07\/2026","result_code":0}}`)
		default:
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		}
	})
	s, _, _ := serwerDoTestow(t, a)
	s.cfg.FormyPlatnosciIDs = nil
	sesja := polaczonyKlient(t, s)
	ctx := context.Background()

	przygotowanie, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25", "forma_platnosci": "przelew",
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	if err != nil || przygotowanie.IsError {
		t.Fatalf("przygotuj_fakture = %v / %s", err, tekstWyniku(t, przygotowanie))
	}
	if !strings.Contains(tekstWyniku(t, przygotowanie), "NIE zostanie zapisana") {
		t.Errorf("podgląd nie ostrzega o braku mapowania:\n%s", tekstWyniku(t, przygotowanie))
	}
	szkic := strukturaWyniku[WyjsciePrzygotuj](t, przygotowanie)

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "zatwierdz_fakture",
		Arguments: map[string]any{"szkic_id": szkic.SzkicID},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("zatwierdz_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}
	if _, ok := a.ostatnieCialo["forma_platnosci"]; ok {
		t.Errorf("wysłano forma_platnosci bez mapowania: %q", a.ostatnieCialo.Get("forma_platnosci"))
	}
}

func TestPoprawnaFormaPlatnosciNieGenerujeOstrzezenia(t *testing.T) {
	a := nowaAtrapa(t, func(a *atrapaSystim, act string, form url.Values, w http.ResponseWriter) {
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
	})
	s, _, _ := serwerDoTestow(t, a)
	sesja := polaczonyKlient(t, s)

	wynik, err := sesja.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta": "41", "data_wystawienia": "2026-07-25", "forma_platnosci": "przelew",
			"pozycje": []map[string]any{{"opis": "U", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"}},
		},
	})
	if err != nil || wynik.IsError {
		t.Fatalf("przygotuj_fakture = %v / %s", err, tekstWyniku(t, wynik))
	}
	if strings.Contains(tekstWyniku(t, wynik), "Forma płatności") {
		t.Errorf("niepotrzebne ostrzeżenie przy poprawnej konfiguracji:\n%s", tekstWyniku(t, wynik))
	}
}
