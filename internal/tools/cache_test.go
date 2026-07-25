package tools

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mariuszkru/mcpsystim/internal/config"
	"github.com/Mariuszkru/mcpsystim/internal/invoicing"
	"github.com/Mariuszkru/mcpsystim/internal/systim"
)

// serwerZCache buduje warstwę narzędzi z włączonym cache kartotek.
//
// serwerDoTestow celowo zostawia cache wyłączony, żeby pozostałe testy widziały
// każde wywołanie API osobno; tutaj sprawdzamy właśnie sam cache.
func serwerZCache(t *testing.T, a *atrapaSystim, ttl time.Duration) *Serwer {
	t.Helper()

	klient, err := systim.NewClient(systim.Opcje{
		Login: "u", Pass: "p", BaseURL: a.srv.URL,
		Timeout: 5 * time.Second, TTLKartotek: ttl,
	})
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
	formy := make(map[string]string, len(systim.IDFormyPlatnosci))
	for nazwa, id := range systim.IDFormyPlatnosci {
		formy[nazwa] = strconv.Itoa(id)
	}
	cfg := &config.Config{
		IDSzablonu:        map[int]string{0: "43", 1: "1"},
		IDNumeracji:       map[int]string{0: "1", 1: "5"},
		FormyPlatnosciIDs: formy,
		KatalogPDF:        t.TempDir(),
		MaxPozycji:        config.DomyslnyMaxPozycji,
	}
	return NowySerwer(klient, stawki, szkice, cfg, nil)
}

// TestPrzygotujFaktureNiePobieraKartotekiPonownie pilnuje głównego zysku z cache:
// szukaj_kontrahenta i przygotuj_fakture czytają tę samą kartotekę, a bez cache
// każde z nich ściągało ją z API osobno.
func TestPrzygotujFaktureNiePobieraKartotekiPonownie(t *testing.T) {
	var pobrania atomic.Int32
	a := nowaAtrapa(t, func(_ *atrapaSystim, act string, _ url.Values, w http.ResponseWriter) {
		if act != "listCompanies" {
			t.Errorf("nieoczekiwana metoda %q", act)
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
			return
		}
		pobrania.Add(1)
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa sp. z o.o.","nip":"1234567890"}}}`)
	})
	sesja := polaczonyKlient(t, serwerZCache(t, a, 5*time.Minute))
	ctx := context.Background()

	if _, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "szukaj_kontrahenta",
		Arguments: map[string]any{"fraza": "Alfa"},
	}); err != nil {
		t.Fatalf("szukaj_kontrahenta = %v", err)
	}

	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name: "przygotuj_fakture",
		Arguments: map[string]any{
			"id_kontrahenta":   "41",
			"data_wystawienia": "2026-07-24",
			"pozycje": []map[string]any{
				{"opis": "Konsultacje", "ilosc": "1", "cena_netto": "100", "stawka_vat": "23"},
			},
		},
	})
	if err != nil {
		t.Fatalf("przygotuj_fakture = %v", err)
	}
	if wynik.IsError {
		t.Fatalf("przygotuj_fakture zwróciło błąd: %s", tekstWyniku(t, wynik))
	}
	// Nazwa nabywcy musi trafić do podglądu — cache nie może jej zgubić.
	if !strings.Contains(tekstWyniku(t, wynik), "Alfa sp. z o.o.") {
		t.Errorf("podgląd nie zawiera nazwy nabywcy:\n%s", tekstWyniku(t, wynik))
	}
	if got := pobrania.Load(); got != 1 {
		t.Errorf("pobrań kartoteki = %d, chcę 1 — przygotuj_fakture ma korzystać z cache", got)
	}
}

// TestSzukajKontrahentaOdswiezaGdyBrakTrafienia pilnuje, żeby cache nie ukrył
// kontrahenta założonego w panelu przed chwilą.
func TestSzukajKontrahentaOdswiezaGdyBrakTrafienia(t *testing.T) {
	var dodany atomic.Bool
	var pobrania atomic.Int32
	a := nowaAtrapa(t, func(_ *atrapaSystim, act string, _ url.Values, w http.ResponseWriter) {
		if act != "listCompanies" {
			t.Errorf("nieoczekiwana metoda %q", act)
			return
		}
		pobrania.Add(1)
		if dodany.Load() {
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"},"77":{"nazwa":"Beta Nowa"}}}`)
			return
		}
		io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
	})
	sesja := polaczonyKlient(t, serwerZCache(t, a, 5*time.Minute))
	ctx := context.Background()

	// Pierwsze wyszukanie buduje cache.
	if _, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "szukaj_kontrahenta",
		Arguments: map[string]any{"fraza": "Alfa"},
	}); err != nil {
		t.Fatalf("szukaj_kontrahenta = %v", err)
	}

	dodany.Store(true)
	wynik, err := sesja.CallTool(ctx, &mcp.CallToolParams{
		Name:      "szukaj_kontrahenta",
		Arguments: map[string]any{"fraza": "Beta"},
	})
	if err != nil {
		t.Fatalf("szukaj_kontrahenta = %v", err)
	}
	if !strings.Contains(tekstWyniku(t, wynik), "Beta Nowa") {
		t.Errorf("nowy kontrahent nie został znaleziony mimo odświeżenia:\n%s", tekstWyniku(t, wynik))
	}
	if got := pobrania.Load(); got != 2 {
		t.Errorf("pobrań kartoteki = %d, chcę 2 (cache + jedno odświeżenie po braku trafienia)", got)
	}
}
