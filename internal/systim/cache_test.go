package systim

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// kartotekaTestowa podnosi serwer odpowiadający podaną kartoteką i zwraca licznik
// wywołań metody listującej wraz z fabryką klientów o zadanym TTL cache.
func kartotekaTestowa(t *testing.T, odpowiedz func() string) (*atomic.Int32, func(ttl time.Duration) *Client) {
	t.Helper()
	var wywolania atomic.Int32
	s := serwerTestowy(t, func(act string, _ url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"TOKEN"}}`)
		case "listCompanies", "listProducts":
			wywolania.Add(1)
			io.WriteString(w, odpowiedz())
		default:
			t.Errorf("nieoczekiwana metoda %q", act)
		}
	})
	zTTL := func(ttl time.Duration) *Client {
		c, err := NewClient(Opcje{
			Login: "api_user", Pass: "tajne_haslo",
			BaseURL: s.URL, Timeout: 5 * time.Second, TTLKartotek: ttl,
		})
		if err != nil {
			t.Fatalf("NewClient = %v", err)
		}
		return c
	}
	return &wywolania, zTTL
}

func TestKartotekaZCacheNiePowtarzaWywolan(t *testing.T) {
	wywolania, zTTL := kartotekaTestowa(t, func() string {
		return `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"},"42":{"nazwa":"Beta"}}}`
	})
	c := zTTL(5 * time.Minute)

	for i := range 3 {
		k, err := c.Kontrahenci(context.Background(), false)
		if err != nil {
			t.Fatalf("Kontrahenci (%d) = %v", i, err)
		}
		if len(k.Rekordy) != 2 {
			t.Fatalf("rekordów = %d, chcę 2", len(k.Rekordy))
		}
		// Pierwszy odczyt idzie do API, kolejne mają pochodzić z cache.
		if czyZCache := k.ZCache; czyZCache != (i > 0) {
			t.Errorf("odczyt %d: ZCache = %v, chcę %v", i, czyZCache, i > 0)
		}
	}
	if got := wywolania.Load(); got != 1 {
		t.Errorf("wywołań listCompanies = %d, chcę 1 — kolejne odczyty mają iść z cache", got)
	}
}

func TestKartotekaOdswiezaSiePoTTL(t *testing.T) {
	wywolania, zTTL := kartotekaTestowa(t, func() string {
		return `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`
	})
	c := zTTL(5 * time.Minute)

	teraz := time.Now()
	c.UstawCzasCacheDoTestow(func() time.Time { return teraz })

	if _, err := c.Kontrahenci(context.Background(), false); err != nil {
		t.Fatalf("pierwszy odczyt = %v", err)
	}
	// Wpis w cache jest jeszcze świeży.
	teraz = teraz.Add(4 * time.Minute)
	if _, err := c.Kontrahenci(context.Background(), false); err != nil {
		t.Fatalf("odczyt w ramach TTL = %v", err)
	}
	if got := wywolania.Load(); got != 1 {
		t.Fatalf("wywołań w ramach TTL = %d, chcę 1", got)
	}
	// Po przekroczeniu TTL kartoteka musi zostać pobrana ponownie.
	teraz = teraz.Add(2 * time.Minute)
	k, err := c.Kontrahenci(context.Background(), false)
	if err != nil {
		t.Fatalf("odczyt po TTL = %v", err)
	}
	if k.ZCache {
		t.Error("ZCache = true po przekroczeniu TTL, chcę świeżego odczytu")
	}
	if got := wywolania.Load(); got != 2 {
		t.Errorf("wywołań po TTL = %d, chcę 2", got)
	}
}

func TestKontrahentPoIDOdswiezaPrzyBrakuTrafienia(t *testing.T) {
	var dodany atomic.Bool
	wywolania, zTTL := kartotekaTestowa(t, func() string {
		if dodany.Load() {
			return `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"},"77":{"nazwa":"Nowy"}}}`
		}
		return `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`
	})
	c := zTTL(5 * time.Minute)

	if _, ok, err := c.KontrahentPoID(context.Background(), "41"); err != nil || !ok {
		t.Fatalf("KontrahentPoID(41) = ok %v, err %v", ok, err)
	}
	if got := wywolania.Load(); got != 1 {
		t.Fatalf("wywołań = %d, chcę 1", got)
	}

	// Kontrahent założony w panelu po zbudowaniu cache musi zostać znaleziony:
	// brak trafienia w danych z cache wymusza jedno odświeżenie.
	dodany.Store(true)
	r, ok, err := c.KontrahentPoID(context.Background(), "77")
	if err != nil {
		t.Fatalf("KontrahentPoID(77) = %v", err)
	}
	if !ok || r.Nazwa() != "Nowy" {
		t.Fatalf("KontrahentPoID(77) = %+v, ok %v — chcę kontrahenta Nowy po odświeżeniu", r, ok)
	}
	if got := wywolania.Load(); got != 2 {
		t.Errorf("wywołań = %d, chcę 2 (odczyt z cache + jedno odświeżenie)", got)
	}

	// Świeża kartoteka nie zawiera ID 99, więc drugiego odświeżenia już nie ma.
	if _, ok, err := c.KontrahentPoID(context.Background(), "99"); err != nil || ok {
		t.Fatalf("KontrahentPoID(99) = ok %v, err %v, chcę braku trafienia", ok, err)
	}
	if got := wywolania.Load(); got != 3 {
		t.Errorf("wywołań = %d, chcę 3 — po świeżym odczycie nie ponawiamy w nieskończoność", got)
	}
}

func TestKartotekaBezCachePytaZaKazdymRazem(t *testing.T) {
	wywolania, zTTL := kartotekaTestowa(t, func() string {
		return `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`
	})
	c := zTTL(0) // cache wyłączony

	for range 3 {
		if _, err := c.Kontrahenci(context.Background(), false); err != nil {
			t.Fatalf("Kontrahenci = %v", err)
		}
	}
	if got := wywolania.Load(); got != 3 {
		t.Errorf("wywołań = %d, chcę 3 — przy TTL 0 cache ma być wyłączony", got)
	}
}

func TestRownolegleOdczytyKartotekiDajaJednoWywolanie(t *testing.T) {
	wywolania, zTTL := kartotekaTestowa(t, func() string {
		// Odpowiedź celowo powolna, żeby żądania faktycznie się nałożyły.
		time.Sleep(50 * time.Millisecond)
		return `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`
	})
	c := zTTL(5 * time.Minute)
	c.UstawTokenDoTestow("TOKEN")

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Kontrahenci(context.Background(), false); err != nil {
				t.Errorf("goroutine %d: Kontrahenci = %v", i, err)
			}
		}()
	}
	wg.Wait()

	if got := wywolania.Load(); got != 1 {
		t.Errorf("wywołań listCompanies = %d, chcę 1 — równoległe odczyty mają się złożyć w jedno pobranie", got)
	}
}
